package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/luci"
)

type probeOptions struct {
	host, password, pin string
	dump                bool
	dumpDir             string
	discover            bool
	outDir              string
}

// newProbeCmd logs in to a real Deco, reads every read-only form, and logs out.
// It never writes to the device. Phase 0 exit criterion: this works on the user's model.
func newProbeCmd() *cobra.Command {
	var o probeOptions
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Log in to a Deco and print its read-only state (Phase 0 spike)",
		Long: `Logs in to the main Deco over its local API, prints devices, clients, WAN,
performance and Wi-Fi as JSON, then logs out. Read-only.

--dump also writes each decrypted response, sanitised (MACs, IPs, names and
identifiers replaced), as a test fixture. --discover-forms crawls the Deco's own
web UI and writes docs/device-forms-<model>-<fw>.md listing every form it uses.

The password is the Deco's local management password. It can also be set with
ATLANTIS_DECO_PASSWORD. On first contact over https the certificate fingerprint
is printed; pass it back with --tls-pin to pin it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.password == "" {
				o.password = os.Getenv("ATLANTIS_DECO_PASSWORD")
			}
			if o.password == "" {
				return errors.New("no password: use --password or ATLANTIS_DECO_PASSWORD")
			}
			if !strings.Contains(o.host, "://") {
				o.host = "http://" + o.host
			}
			return runProbe(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", "", "Deco address, for example 192.168.68.1 (required)")
	f.StringVar(&o.password, "password", "", "local management password (or ATLANTIS_DECO_PASSWORD)")
	f.StringVar(&o.pin, "tls-pin", "", "hex SHA-256 of the device certificate, for https")
	f.BoolVar(&o.dump, "dump", false, "write sanitised response fixtures")
	f.StringVar(&o.dumpDir, "dump-dir", "internal/deco/luci/testdata", "directory for --dump fixtures")
	f.BoolVar(&o.discover, "discover-forms", false, "crawl the device web UI and write the device-forms doc")
	f.StringVar(&o.outDir, "out-dir", "docs", "directory for the --discover-forms doc")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

// probeStep is one read-only form the probe calls.
type probeStep struct {
	name, area, form string
	params           any
	fn               func(ctx context.Context, c *luci.Client) (any, error)
}

var probeSteps = []probeStep{
	{"devices", "device", "device_list", nil, func(ctx context.Context, c *luci.Client) (any, error) { return c.Devices(ctx) }},
	{"clients", "client", "client_list", map[string]string{"device_mac": "default"}, func(ctx context.Context, c *luci.Client) (any, error) { return c.Clients(ctx, "") }},
	{"wan", "network", "wan_ipv4", nil, func(ctx context.Context, c *luci.Client) (any, error) { return c.WAN(ctx) }},
	{"performance", "network", "performance", nil, func(ctx context.Context, c *luci.Client) (any, error) { return c.Performance(ctx) }},
	{"wireless", "wireless", "wlan", nil, func(ctx context.Context, c *luci.Client) (any, error) { return c.Wireless(ctx) }},
}

func runProbe(ctx context.Context, o probeOptions) error {
	c, err := luci.New(o.host, o.password, luci.Options{
		Pin: o.pin,
		OnFingerprint: func(fp string) {
			fmt.Fprintf(os.Stderr, "TLS certificate fingerprint (pin with --tls-pin): %s\n", fp)
		},
		OnSchemeChange: func(b string) {
			fmt.Fprintf(os.Stderr, "device redirected to https: %s\n", b)
		},
	})
	if err != nil {
		return err
	}
	if err := c.Login(ctx); err != nil {
		return explainLoginError(err)
	}
	defer func() {
		if err := c.Logout(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "logout: %v\n", err)
		}
	}()

	out := map[string]any{"base_url": c.BaseURL()}
	verified := map[string]string{}
	var failed []string
	var devices []deco.Device
	for _, s := range probeSteps {
		v, err := s.fn(ctx, c)
		key := s.area + "/" + s.form
		if err != nil {
			out[s.name] = map[string]string{"error": err.Error()}
			verified[key] = "failed"
			failed = append(failed, s.name)
			continue
		}
		out[s.name] = v
		verified[key] = "ok"
		if d, ok := v.([]deco.Device); ok {
			devices = d
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}

	if o.dump {
		failed = append(failed, dumpFixtures(ctx, c, o.dumpDir)...)
	}
	if o.discover {
		if err := writeFormsDoc(ctx, c, o.outDir, devices, verified); err != nil {
			fmt.Fprintf(os.Stderr, "discover-forms: %v\n", err)
			failed = append(failed, "discover-forms")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("probe: %d step(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// dumpFixtures writes one sanitised fixture per form and returns the names of failed dumps.
func dumpFixtures(ctx context.Context, c *luci.Client, dir string) []string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "dump: %v\n", err)
		return []string{"dump"}
	}
	san := luci.NewSanitizer()
	var failed []string
	written := 0
	for _, s := range probeSteps {
		raw, err := c.RawParams(ctx, s.area, s.form, s.params)
		if err == nil {
			raw, err = san.Sanitize(raw)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, s.area+"_"+s.form+".json"), append(raw, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "dump %s/%s: %v\n", s.area, s.form, err)
			failed = append(failed, "dump:"+s.name)
			continue
		}
		written++
	}
	fmt.Fprintf(os.Stderr, "wrote %d sanitised fixture(s) to %s; review them before committing\n", written, dir)
	return failed
}

func writeFormsDoc(ctx context.Context, c *luci.Client, dir string, devices []deco.Device, verified map[string]string) error {
	rep, err := c.DiscoverForms(ctx)
	if err != nil {
		return err
	}
	model, fw := "unknown", "unknown"
	for _, d := range devices {
		if d.Role == "master" || model == "unknown" {
			model, fw = d.Model, d.SoftwareVer
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("device-forms-%s-%s.md", slug(model), slug(fw)))
	if err := os.WriteFile(path, []byte(rep.Markdown(model, fw, verified)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d forms from %d web UI files, %d failed)\n", path, len(rep.Forms), rep.Fetched, rep.Failed)
	for _, e := range rep.FailSamples {
		fmt.Fprintf(os.Stderr, "  fetch failed: %s\n", e)
	}
	return nil
}

var nonSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes a lowercase, hyphenated file-name fragment.
func slug(s string) string {
	s = strings.Trim(nonSlugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "unknown"
	}
	return s
}

func explainLoginError(err error) error {
	switch {
	case errors.Is(err, deco.ErrWrongPassword):
		return fmt.Errorf("device rejected the password; stop and check it before retrying (the device locks out after repeated failures): %w", err)
	case errors.Is(err, deco.ErrOtherAdmin):
		return fmt.Errorf("another admin session is active (the Deco app or a browser): %w", err)
	case errors.Is(err, deco.ErrCertChanged):
		return fmt.Errorf("TLS certificate changed since it was pinned: %w", err)
	}
	return err
}
