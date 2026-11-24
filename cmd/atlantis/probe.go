package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/luci"
)

// newProbeCmd logs in to a real Deco, reads every read-only form, and logs out.
// It never writes to the device. Phase 0 exit criterion: this works on the user's model.
func newProbeCmd() *cobra.Command {
	var host, password, pin string
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Log in to a Deco and print its read-only state (Phase 0 spike)",
		Long: `Logs in to the main Deco over its local API, prints devices, clients, WAN,
performance and Wi-Fi as JSON, then logs out. Read-only.

The password is the Deco's local management password. It can also be set with
ATLANTIS_DECO_PASSWORD. On first contact over https the certificate fingerprint
is printed; pass it back with --tls-pin to pin it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if password == "" {
				password = os.Getenv("ATLANTIS_DECO_PASSWORD")
			}
			if password == "" {
				return errors.New("no password: use --password or ATLANTIS_DECO_PASSWORD")
			}
			if !strings.Contains(host, "://") {
				host = "http://" + host
			}
			return runProbe(cmd.Context(), host, password, pin)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "Deco address, for example 192.168.68.1 (required)")
	cmd.Flags().StringVar(&password, "password", "", "local management password (or ATLANTIS_DECO_PASSWORD)")
	cmd.Flags().StringVar(&pin, "tls-pin", "", "hex SHA-256 of the device certificate, for https")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func runProbe(ctx context.Context, host, password, pin string) error {
	c, err := luci.New(host, password, luci.Options{
		Pin: pin,
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

	steps := []struct {
		name string
		fn   func() (any, error)
	}{
		{"devices", func() (any, error) { return c.Devices(ctx) }},
		{"clients", func() (any, error) { return c.Clients(ctx, "") }},
		{"wan", func() (any, error) { return c.WAN(ctx) }},
		{"performance", func() (any, error) { return c.Performance(ctx) }},
		{"wireless", func() (any, error) { return c.Wireless(ctx) }},
	}
	out := map[string]any{"base_url": c.BaseURL()}
	var failed []string
	for _, s := range steps {
		v, err := s.fn()
		if err != nil {
			out[s.name] = map[string]string{"error": err.Error()}
			failed = append(failed, s.name)
			continue
		}
		out[s.name] = v
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("probe: %d form(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
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
