package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/deco/tmpclient"
)

// newSSHClientRenameProbeCmd proves renaming a client on real hardware: it
// renames one client to a test name (ASCII, then Hebrew), confirms the Deco
// shows it, and puts the original name back.
func newSSHClientRenameProbeCmd() *cobra.Command {
	var o sshProbeOptions
	var mac, out string
	var names []string
	cmd := &cobra.Command{
		Use:   "client-rename-probe",
		Short: "Verify renaming a client on a Deco, then restore its name",
		Long: `Renames one client (by MAC) to a test name, confirms the Deco reports it,
waits, checks again, and restores the original name. By default it tries an
ASCII name and a Hebrew one. The client's name in the Deco app changes for
about half a minute. A sanitised record is written to --out for the tests.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.email == "" {
				o.email = os.Getenv("ATLANTIS_CLOUD_EMAIL")
			}
			o.password = os.Getenv("ATLANTIS_CLOUD_PASSWORD")
			o.in, o.out, o.errOut = os.Stdin, os.Stdout, os.Stderr
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			c, err := sshSession(ctx, o)
			if err != nil {
				return err
			}
			defer func() { _ = c.Logout(context.Background()) }()
			return clientRenameProbe(ctx, c, mac, names, out, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", "", "Deco IP address on the LAN (required)")
	f.StringVar(&o.email, "email", "", "TP-Link ID email (or ATLANTIS_CLOUD_EMAIL)")
	f.StringVar(&o.hostKey, "host-key", "", "expected SSH host key fingerprint (SHA256:...)")
	f.StringVar(&o.knownHosts, "known-hosts", "", "trusted host keys file (default: ssh_known_hosts in the user config dir)")
	f.IntVar(&o.port, "port", 0, "SSH port (default: from TDP, else 20001)")
	f.StringVar(&mac, "mac", "", "the client's MAC address (required)")
	f.StringSliceVar(&names, "name", nil, "test names (default: <current>-t and a Hebrew name)")
	f.StringVar(&out, "out", "internal/deco/tmpclient/testdata/m5", "directory for the sanitised record")
	f.BoolVar(&o.debug, "debug", false, "print discovery and transport details (never secrets)")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("mac")
	return cmd
}

type renameRecord struct {
	Fields []string `json:"fields"` // the client entry's fields (values redacted)
	Tried  []string `json:"tried"`  // what each test name was: ascii or hebrew
	Result string   `json:"result"`
}

func clientRenameProbe(ctx context.Context, c *tmpclient.Client, mac string, names []string, out string, o sshProbeOptions) error {
	entry, err := c.ClientEntry(ctx, mac)
	if err != nil {
		return err
	}
	orig := b64Plain(entry.Str("name"))
	if len(names) == 0 {
		names = []string{trimBytes(orig, 62) + "-t", "בדיקה-t"}
	}
	// The undo must be valid before anything changes.
	if err := tmpclient.ValidateClientName(orig); err != nil {
		return fmt.Errorf("the current name could not be put back (%w), so nothing was changed", err)
	}
	for _, n := range names {
		if err := tmpclient.ValidateClientName(n); err != nil {
			return err
		}
	}
	rec := renameRecord{}
	for k := range entry {
		rec.Fields = append(rec.Fields, k)
	}
	sort.Strings(rec.Fields)

	sent, restored := false, true
	defer func() {
		if sent && !restored {
			if err := c.ProbeRenameClient(context.Background(), mac, orig, true); err != nil {
				fmt.Fprintf(o.errOut, "RESTORE FAILED: %v\nRename the device back in the Deco app.\n", err)
			}
		}
	}()
	for _, n := range names {
		kind := "ascii"
		for _, r := range n {
			if r >= utf8.RuneSelf {
				kind = "hebrew"
				break
			}
		}
		fmt.Fprintf(o.out, "Renaming the client (%s test name).\n", kind)
		sent, restored = true, false
		if err := c.ProbeRenameClient(ctx, mac, n, true); err != nil {
			rec.Result = "rename failed: " + err.Error()
			_ = writeRenameRecord(out, rec)
			return err
		}
		if err := sleepCtx(ctx, probeSettle); err != nil {
			return err
		}
		if e, err := c.ClientEntry(ctx, mac); err != nil || b64Plain(e.Str("name")) != n {
			rec.Result = fmt.Sprintf("%s name did not persist", kind)
			_ = writeRenameRecord(out, rec)
			return fmt.Errorf("the %s name did not persist (%v)", kind, err)
		}
		fmt.Fprintln(o.out, "Confirmed. Restoring the original name.")
		if err := c.ProbeRenameClient(ctx, mac, orig, true); err != nil {
			rec.Result = "restore failed: " + err.Error()
			_ = writeRenameRecord(out, rec)
			return err
		}
		restored = true
		rec.Tried = append(rec.Tried, kind)
	}
	if e, err := c.ClientEntry(ctx, mac); err != nil || b64Plain(e.Str("name")) != orig {
		rec.Result = "the original name is not back"
		_ = writeRenameRecord(out, rec)
		return fmt.Errorf("the original name is not back (%v)", err)
	}
	rec.Result = "pass"
	if err := writeRenameRecord(out, rec); err != nil {
		return err
	}
	fmt.Fprintln(o.out, "PASS: renaming works on this Deco and the original name is back.")
	return nil
}

// trimBytes shortens s to at most n bytes without splitting a character.
func trimBytes(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func writeRenameRecord(dir string, rec renameRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "client_rename_probe.json"), append(b, '\n'), 0o644)
}
