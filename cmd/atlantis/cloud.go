package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/t0mer/atlantis/internal/deco/cloud"
	"github.com/t0mer/atlantis/internal/deco/fixture"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
	"github.com/t0mer/atlantis/internal/tplinkcloud"
)

type cloudProbeOptions struct {
	email, site, dumpDir, terminalFile, caFile string
	dump, debug                                bool

	// Set by tests only; there are no flags for these.
	bootstrapURL string
	allowHTTP    bool
	password     string
	keys         tplinkcloud.AppKeys
	in           io.Reader
	out, errOut  io.Writer
}

func newCloudCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cloud", Short: "Reach Decos through the TP-Link cloud, like the Deco app"}
	cmd.AddCommand(newCloudProbeCmd())
	return cmd
}

func newCloudProbeCmd() *cobra.Command {
	var o cloudProbeOptions
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Sign in with a TP-Link ID, list the Deco sites, and read one site",
		Long: `Signs in to the TP-Link cloud with your TP-Link ID and lists the Deco sites
on the account. With --site it connects to that site's main Deco through the
cloud relay, prints the read-only state as JSON (secrets redacted), and
disconnects. It never sends a write.

The cloud API needs the Deco app's access key and secret, which Atlantis does
not ship: set ATLANTIS_CLOUD_ACCESS_KEY and ATLANTIS_CLOUD_SECRET. The
password is read from ATLANTIS_CLOUD_PASSWORD or prompted for without echo.
A two-step verification code is prompted for when the account needs one.

--dump writes each response, sanitised (MACs, IPs, names and identifiers
replaced), as a test fixture.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.keys = tplinkcloud.AppKeys{
				AccessKey: os.Getenv("ATLANTIS_CLOUD_ACCESS_KEY"),
				Secret:    os.Getenv("ATLANTIS_CLOUD_SECRET"),
			}
			if o.email == "" {
				o.email = os.Getenv("ATLANTIS_CLOUD_EMAIL")
			}
			if o.email == "" {
				return errors.New("no TP-Link ID: use --email or ATLANTIS_CLOUD_EMAIL")
			}
			o.password = os.Getenv("ATLANTIS_CLOUD_PASSWORD")
			if o.caFile == "" {
				o.caFile = defaultCAFile()
			}
			o.in, o.out, o.errOut = os.Stdin, os.Stdout, os.Stderr
			return runCloudProbe(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.email, "email", "", "TP-Link ID email (or ATLANTIS_CLOUD_EMAIL)")
	f.StringVar(&o.site, "site", "", "site ID from the list to read; omit to only list sites")
	f.BoolVar(&o.dump, "dump", false, "write sanitised response fixtures (needs --site)")
	f.BoolVar(&o.debug, "debug", false, "print cloud relay status codes (never tokens or data)")
	f.StringVar(&o.dumpDir, "dump-dir", "internal/deco/cloud/testdata", "directory for --dump fixtures")
	f.StringVar(&o.terminalFile, "terminal-file", "", "where the installation's terminal ID is kept (default: user config dir)")
	f.StringVar(&o.caFile, "ca-file", "", "extra PEM root certificates for the cloud TLS connection (default: "+caFileName+" in the user config dir, if present)")
	return cmd
}

func runCloudProbe(ctx context.Context, o cloudProbeOptions) error {
	termID, err := loadTerminalID(o.terminalFile)
	if err != nil {
		return err
	}
	opts := tplinkcloud.Options{Keys: o.keys, TerminalUUID: termID, BootstrapURL: o.bootstrapURL, AllowHTTP: o.allowHTTP}
	if o.caFile != "" {
		pem, err := os.ReadFile(o.caFile)
		if err != nil {
			return err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("no certificates in %s", o.caFile)
		}
		opts.RootCAs = pool
	}
	if o.caFile != "" {
		fmt.Fprintf(o.errOut, "using CA certificates from %s\n", o.caFile)
	}
	acct, err := tplinkcloud.New(opts)
	if err != nil {
		return fmt.Errorf("%w (set ATLANTIS_CLOUD_ACCESS_KEY and ATLANTIS_CLOUD_SECRET)", err)
	}
	in := bufio.NewReader(o.in)
	if o.password == "" {
		if o.password, err = readSecret(o.in, in, o.errOut, "TP-Link ID password: "); err != nil {
			return err
		}
	}
	if err := cloudLogin(ctx, acct, o, in); err != nil {
		if strings.Contains(err.Error(), "certificate signed by unknown authority") {
			return fmt.Errorf("%w\n(TP-Link's cloud uses its own root CA: put it in %s, or pass --ca-file)", err, caFileName)
		}
		return err
	}

	sites, err := acct.Sites(ctx)
	if err != nil {
		return err
	}
	if o.debug {
		for _, st := range sites {
			fmt.Fprintf(o.errOut, "debug: site %s: accessPlatform=%q operation_mode=%q\n", st.Name, st.AccessPlatform, st.OperationMode)
		}
	}
	if o.site == "" {
		printSites(o.out, sites)
		return nil
	}
	i := slices.IndexFunc(sites, func(s tplinkcloud.Site) bool { return s.ID == o.site })
	if i < 0 {
		printSites(o.errOut, sites)
		return fmt.Errorf("no site with ID %q on this account", o.site)
	}
	return probeSite(ctx, acct, sites[i], o)
}

// cloudLogin signs in, completing two-step verification interactively.
func cloudLogin(ctx context.Context, acct *tplinkcloud.Client, o cloudProbeOptions, in *bufio.Reader) error {
	err := acct.Login(ctx, o.email, o.password)
	var mfa *tplinkcloud.MFAChallenge
	if !errors.As(err, &mfa) {
		if errors.Is(err, tplinkcloud.ErrAuthFailed) {
			return fmt.Errorf("TP-Link rejected the sign-in; check the email and password before retrying (repeated failures lock the account): %w", err)
		}
		return err
	}
	typ := tplinkcloud.MFATOTP
	switch {
	case slices.Contains(mfa.Types, tplinkcloud.MFAEmail):
		typ = tplinkcloud.MFAEmail
		if err := acct.SendMFACode(ctx, typ); err != nil {
			return fmt.Errorf("requesting a verification code: %w", err)
		}
		fmt.Fprintf(o.errOut, "A verification code was sent to %s.\n", mfa.MaskedEmail)
	case slices.Contains(mfa.Types, tplinkcloud.MFATOTP):
		fmt.Fprintln(o.errOut, "Two-step verification: use the code from your authenticator app.")
	default:
		return fmt.Errorf("two-step verification type(s) %v are not supported yet", mfa.Types)
	}
	fmt.Fprint(o.errOut, "Verification code: ")
	code, err := in.ReadString('\n')
	if err != nil && code == "" {
		return fmt.Errorf("reading the verification code: %w", err)
	}
	if err := acct.VerifyMFA(ctx, typ, strings.TrimSpace(code)); err != nil {
		return fmt.Errorf("two-step verification failed: %w", err)
	}
	return nil
}

func printSites(w io.Writer, sites []tplinkcloud.Site) {
	if len(sites) == 0 {
		fmt.Fprintln(w, "No Deco sites on this account.")
		return
	}
	fmt.Fprintf(w, "%-34s %-8s %-6s %-6s %s\n", "SITE ID", "STATE", "ROLE", "NODES", "NAME")
	for _, s := range sites {
		state, role := "offline", "shared"
		if s.Online {
			state = "online"
		}
		if s.Owner {
			role = "owner"
		}
		fmt.Fprintf(w, "%-34s %-8s %-6s %-6d %s\n", s.ID, state, role, len(s.Nodes), s.Name)
	}
	fmt.Fprintln(w, "\nRead one site with: atlantis cloud probe --site <SITE ID>")
}

type cloudStep struct {
	file, op string
	params   any
}

// probeSite connects to one site and runs every allowlisted read.
func probeSite(ctx context.Context, acct *tplinkcloud.Client, site tplinkcloud.Site, o cloudProbeOptions) error {
	c := cloud.New(acct, site)
	if o.debug {
		c.SetDebug(func(f string, a ...any) { fmt.Fprintf(o.errOut, "debug: "+f+"\n", a...) })
	}
	if err := c.Login(ctx); err != nil {
		return fmt.Errorf("connecting to %s through the cloud: %w", site.Name, err)
	}
	defer func() {
		if err := c.Logout(ctx); err != nil {
			fmt.Fprintf(o.errOut, "disconnect: %v\n", err)
		}
	}()
	return readSession(ctx, c, sessionOutput{out: o.out, errOut: o.errOut, dump: o.dump, dumpDir: o.dumpDir})
}

// sessionOutput is where readSession prints and dumps.
type sessionOutput struct {
	out, errOut io.Writer
	dump        bool
	dumpDir     string
}

// readSession runs every allowlisted read on an open session client, prints
// the redacted results as JSON and optionally dumps sanitised fixtures. It
// is shared by the cloud and SSH probes.
func readSession(ctx context.Context, c *tmpclient.Client, o sessionOutput) error {
	out := map[string]any{}
	if neg, err := fixture.Redact(c.Negotiation()); err == nil {
		out["COMP_NEGOTIATE"] = neg
	}
	results := map[string]json.RawMessage{}
	var failed []string
	run := func(s cloudStep) {
		raw, err := c.RawParams(ctx, tmpclient.RawArea, s.op, s.params)
		if err != nil {
			out[s.file] = map[string]string{"error": err.Error()}
			failed = append(failed, s.file)
			return
		}
		out[s.file], results[s.file] = raw, raw
	}

	run(cloudStep{"DEVICE_LIST_GET", "DEVICE_LIST_GET", nil})
	master, nodes := deviceIDs(results["DEVICE_LIST_GET"])
	steps := []cloudStep{
		{"CLIENT_LIST_GET", "CLIENT_LIST_GET", nil},
		{"CLIENT_LIST_SPEED_GET", "CLIENT_LIST_SPEED_GET", nil},
		{"WAN_INFO_GET", "WAN_INFO_GET", nil},
		{"WIRELESS_GET", "WIRELESS_GET", nil},
		{"FW_LATEST_GET", "FW_LATEST_GET", nil},
		{"BLOCKED_LIST_GET", "BLOCKED_LIST_GET", nil},
	}
	if master != "" {
		steps = append(steps, cloudStep{"DEVICE_RESOURCE_INFO_GET", "DEVICE_RESOURCE_INFO_GET",
			map[string]any{"device_list": []map[string]string{{"device_id": master}}}})
	}
	for i, id := range nodes {
		steps = append(steps, cloudStep{fmt.Sprintf("DEVICE_SIGNAL_LEVEL_LIST_GET_%d", i+1),
			"DEVICE_SIGNAL_LEVEL_LIST_GET", map[string]string{"device_id": id}})
	}
	for _, s := range steps {
		run(s)
	}

	enc := json.NewEncoder(o.out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if o.dump {
		if err := dumpCloudFixtures(o.dumpDir, results); err != nil {
			fmt.Fprintf(o.errOut, "dump: %v\n", err)
			failed = append(failed, "dump")
		} else {
			fmt.Fprintf(o.errOut, "wrote %d sanitised fixture(s) to %s; review them before committing\n", len(results), o.dumpDir)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("probe: %d step(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// deviceIDs returns the master's device_id and the other nodes' IDs from a
// DEVICE_LIST_GET result. Unknown shapes yield nothing.
func deviceIDs(raw json.RawMessage) (master string, others []string) {
	var r struct {
		DeviceList []struct {
			DeviceID string `json:"device_id"`
			Role     string `json:"role"`
		} `json:"device_list"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", nil
	}
	for _, d := range r.DeviceList {
		switch {
		case d.DeviceID == "":
		case strings.EqualFold(d.Role, "master") && master == "":
			master = d.DeviceID
		default:
			others = append(others, d.DeviceID)
		}
	}
	return master, others
}

func dumpCloudFixtures(dir string, results map[string]json.RawMessage) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	san := fixture.NewSanitizer()
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	slices.Sort(names) // stable placeholder numbering across runs
	for _, n := range names {
		clean, err := san.Sanitize(results[n])
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tmp_"+strings.ToLower(n)+".json"), append(clean, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// caFileName is the TP-Link cloud CA bundle looked for in the user config
// dir. TP-Link's cloud chains to its own root, which system stores lack.
const caFileName = "tplink-cloud-ca.pem"

// defaultCAFile returns <config dir>/atlantis/tplink-cloud-ca.pem if it
// exists, else "".
func defaultCAFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "atlantis", caFileName)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// loadTerminalID returns the persisted terminal UUID, creating it once. The
// cloud remembers trusted terminals for two-step verification, so it must
// stay stable across runs.
func loadTerminalID(path string) (string, error) {
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("finding the config dir (use --terminal-file): %w", err)
		}
		path = filepath.Join(dir, "atlantis", "terminal-id")
	}
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id := tplinkcloud.NewTerminalUUID()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// readSecret prompts without echo on a terminal, else reads a line.
func readSecret(raw io.Reader, buffered *bufio.Reader, prompt io.Writer, label string) (string, error) {
	if f, ok := raw.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(prompt, label)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := buffered.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no password: set ATLANTIS_CLOUD_PASSWORD or run in a terminal")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
