package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
)

// newSSHWiFiProbeCmd proves one Wi-Fi write on real hardware: it changes a
// single field of one network, confirms the Deco shows it, then puts the
// original value back. Only after a probe passes is that field added to
// tmpclient's verifiedWrites.
func newSSHWiFiProbeCmd() *cobra.Command {
	var o sshProbeOptions
	var network, field, out string
	var bands []string
	cmd := &cobra.Command{
		Use:   "wifi-probe",
		Short: "Verify one Wi-Fi write on a Deco, then restore the original setting",
		Long: `Changes one field of one Wi-Fi network (guest, iot or host), confirms the Deco
reports the change, waits, checks again, and restores the original value.

Fields: enable (guest and iot only; an IoT network is only probed while it is
off), ssid, password, encryption (only from an open network, using a temporary
password), hidden. Devices on that network may disconnect briefly.

A sanitised before/after record is written to --out for the tests.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.email == "" {
				o.email = os.Getenv("ATLANTIS_CLOUD_EMAIL")
			}
			o.password = os.Getenv("ATLANTIS_CLOUD_PASSWORD")
			o.in, o.out, o.errOut = os.Stdin, os.Stdout, os.Stderr
			// Ctrl-C ends the wait early; the restore still runs.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			c, err := sshSession(ctx, o)
			if err != nil {
				return err
			}
			defer func() { _ = c.Logout(context.Background()) }()
			return wifiProbe(ctx, c, network, field, bands, out, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", "", "Deco IP address on the LAN (required)")
	f.StringVar(&o.email, "email", "", "TP-Link ID email (or ATLANTIS_CLOUD_EMAIL)")
	f.StringVar(&o.hostKey, "host-key", "", "expected SSH host key fingerprint (SHA256:...)")
	f.StringVar(&o.knownHosts, "known-hosts", "", "trusted host keys file (default: ssh_known_hosts in the user config dir)")
	f.IntVar(&o.port, "port", 0, "SSH port (default: from TDP, else 20001)")
	f.StringVar(&network, "network", "guest", "guest, iot or host")
	f.StringVar(&field, "field", "", "enable, ssid, password, encryption or hidden (required)")
	f.StringSliceVar(&bands, "band", nil, "only these bands (default: every band with the network)")
	f.StringVar(&out, "out", "internal/deco/tmpclient/testdata/m5", "directory for the sanitised record")
	f.BoolVar(&o.debug, "debug", false, "print discovery and transport details (never secrets)")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("field")
	return cmd
}

// probeSettle is how long the Deco gets to apply a change before it is
// checked again.
var probeSettle = 10 * time.Second

type wifiStep struct {
	Patch []string       `json:"patch"`
	Bands []string       `json:"bands"`
	After map[string]any `json:"after"` // band -> sanitised network object
}

type probeRecord struct {
	Network string         `json:"network"`
	Field   string         `json:"field"`
	Before  map[string]any `json:"before"`
	Change  wifiStep       `json:"change"`
	Restore wifiStep       `json:"restore"`
	Result  string         `json:"result"`
}

func wifiProbe(ctx context.Context, c *tmpclient.Client, network, field string, bands []string, out string, o sshProbeOptions) error {
	if !slices.Contains([]string{"guest", "iot", "host"}, network) {
		return fmt.Errorf("--network must be guest, iot or host")
	}
	before, err := networkObjs(ctx, c, network, bands)
	if err != nil {
		return err
	}
	if len(bands) == 0 {
		for b := range before {
			bands = append(bands, b)
		}
		slices.Sort(bands)
	}
	change, restore, err := probePatches(network, field, before)
	if err != nil {
		return err
	}
	change.Bands, restore.Bands = bands, bands
	// Both directions must be valid before anything is sent, or a change
	// could be made that cannot be undone.
	if err := tmpclient.ValidatePatch(change); err != nil {
		return fmt.Errorf("the change is not valid: %w", err)
	}
	if err := tmpclient.ValidatePatch(restore); err != nil {
		return fmt.Errorf("the original value could not be restored afterwards (%w), so nothing was changed", err)
	}
	rec := probeRecord{Network: network, Field: field, Before: sanitise(before)}

	fmt.Fprintf(o.out, "Probing %s %s on %v: changing it now.\n", network, field, bands)
	sent, restored := false, false
	defer func() { // put it back after a failure or Ctrl-C, once something was sent
		if sent && !restored {
			if _, err := c.ProbeNetwork(context.Background(), restore); err != nil {
				fmt.Fprintf(o.errOut, "RESTORE FAILED: %v\nFix %s %s in the Deco app.\n", err, network, field)
			}
		}
	}()
	sent = true
	if _, err := c.ProbeNetwork(ctx, change); err != nil {
		sent = !errors.Is(err, deco.ErrInvalid) // refused before sending
		rec.Result = "change failed: " + err.Error()
		_ = writeRecord(out, rec)
		return fmt.Errorf("change: %w", err)
	}
	// The Deco applies Wi-Fi changes in the background; check it still
	// shows the change once that has settled.
	if err := sleepCtx(ctx, probeSettle); err != nil {
		return err
	}
	after, err := networkObjs(ctx, c, network, bands)
	if err != nil {
		return err
	}
	rec.Change = wifiStep{Patch: change.Fields(), Bands: bands, After: sanitise(after)}
	if err := matches(after, change); err != nil {
		rec.Result = "change did not persist: " + err.Error()
		_ = writeRecord(out, rec)
		return err
	}
	fmt.Fprintln(o.out, "Change confirmed. Restoring the original setting.")
	if _, err := c.ProbeNetwork(ctx, restore); err != nil {
		rec.Result = "restore failed: " + err.Error()
		_ = writeRecord(out, rec)
		return fmt.Errorf("restore: %w", err)
	}
	restored = true
	if err := sleepCtx(ctx, probeSettle/2); err != nil {
		return err
	}
	final, err := networkObjs(ctx, c, network, bands)
	if err != nil {
		return err
	}
	rec.Restore = wifiStep{Patch: restore.Fields(), Bands: bands, After: sanitise(final)}
	if err := matches(final, restore); err != nil {
		rec.Result = "restore did not persist: " + err.Error()
		_ = writeRecord(out, rec)
		return err
	}
	rec.Result = "pass"
	if err := writeRecord(out, rec); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "PASS: %s %s works on this Deco and is back to its original value.\n", network, field)
	return nil
}

// networkObjs reads the network's object on each band (all bands that have
// it when bands is empty).
func networkObjs(ctx context.Context, c *tmpclient.Client, network string, bands []string) (map[string]deco.Obj, error) {
	raw, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		return nil, err
	}
	top, _ := deco.ParseObj(raw)
	out := map[string]deco.Obj{}
	for _, b := range deco.Bands {
		if len(bands) > 0 && !slices.Contains(bands, b) {
			continue
		}
		if n, ok := deco.ParseObj(top.Obj(b)[network]); ok {
			out[b] = n
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the Deco reports no %s network on %v", network, bands)
	}
	return out, nil
}

// probePatches builds the change and the patch that undoes it. All bands
// must agree on the field's current value so one restore fits all.
func probePatches(network, field string, before map[string]deco.Obj) (change, restore deco.WirelessPatch, err error) {
	change.Network, restore.Network = network, network
	var first deco.Obj
	for _, n := range before {
		first = n
		break
	}
	same := func(get func(deco.Obj) string) error {
		v := get(first)
		for b, n := range before {
			if get(n) != v {
				return fmt.Errorf("%s differs between bands (%s); probe one band at a time with --band", field, b)
			}
		}
		return nil
	}
	switch field {
	case deco.FieldEnable:
		if network == "host" {
			return change, restore, errors.New("the main network is never turned off")
		}
		if err := same(func(n deco.Obj) string { return fmt.Sprint(n.Bool("enable")) }); err != nil {
			return change, restore, err
		}
		cur := first.Bool("enable")
		if network == "iot" && cur {
			return change, restore, errors.New("the IoT network is on; probing would disconnect its devices, so only an IoT network that is off is probed")
		}
		change.Enable, restore.Enable = ptrTo(!cur), ptrTo(cur)
	case deco.FieldSSID:
		if err := same(func(n deco.Obj) string { return n.Str("ssid") }); err != nil {
			return change, restore, err
		}
		cur := b64Plain(first.Str("ssid"))
		next := cur + "-t"
		if len(next) > 32 {
			next = cur[:30] + "-t"
		}
		change.SSID, restore.SSID = &next, &cur
	case deco.FieldPassword:
		if err := same(func(n deco.Obj) string { return n.Str("password") }); err != nil {
			return change, restore, err
		}
		cur := b64Plain(first.Str("password"))
		if cur == "" || first.Str("encryption_mode") == "none" {
			return change, restore, errors.New("the network is open; probe encryption instead")
		}
		tmpPass := "atlantis-probe-" + randHex(4)
		change.Password, restore.Password = &tmpPass, &cur
	case deco.FieldEncryption:
		if err := same(func(n deco.Obj) string { return n.Str("encryption_mode") + n.Str("password") }); err != nil {
			return change, restore, err
		}
		if first.Str("encryption_mode") != "none" {
			return change, restore, errors.New("only an open network is probed for security (it is secured with a temporary password, then opened again)")
		}
		tmpPass := "atlantis-probe-" + randHex(4)
		curPass := b64Plain(first.Str("password"))
		if curPass == "" {
			return change, restore, errors.New("the open network has no stored password to put back, so the temporary one would stay; set one in the Deco app first")
		}
		change.Encryption, change.Password = ptrTo("wpa2"), &tmpPass
		restore.Encryption, restore.Password = ptrTo("none"), &curPass
	case deco.FieldHidden:
		if !first.Has("enable_hide_ssid") {
			return change, restore, fmt.Errorf("the Deco does not report hiding for the %s network", network)
		}
		if err := same(func(n deco.Obj) string { return fmt.Sprint(n.Bool("enable_hide_ssid")) }); err != nil {
			return change, restore, err
		}
		cur := first.Bool("enable_hide_ssid")
		change.Hidden, restore.Hidden = ptrTo(!cur), ptrTo(cur)
	default:
		return change, restore, fmt.Errorf("unknown --field %q", field)
	}
	return change, restore, nil
}

// matches checks each band shows the patch.
func matches(nets map[string]deco.Obj, p deco.WirelessPatch) error {
	for b, n := range nets {
		switch {
		case p.Enable != nil && n.Bool("enable") != *p.Enable,
			p.SSID != nil && b64Plain(n.Str("ssid")) != *p.SSID,
			p.Password != nil && b64Plain(n.Str("password")) != *p.Password,
			p.Encryption != nil && n.Str("encryption_mode") != *p.Encryption,
			p.Hidden != nil && n.Bool("enable_hide_ssid") != *p.Hidden:
			return fmt.Errorf("%s does not show %v", b, p.Fields())
		}
	}
	return nil
}

// sanitise drops SSIDs and passwords from a record.
func sanitise(nets map[string]deco.Obj) map[string]any {
	out := map[string]any{}
	for b, n := range nets {
		m := map[string]any{}
		for k, v := range n {
			if k == "ssid" || k == "password" {
				m[k] = "[redacted]"
				continue
			}
			m[k] = v
		}
		out[b] = m
	}
	return out
}

func writeRecord(dir string, rec probeRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("wifi_probe_%s_%s.json", rec.Network, rec.Field)), append(b, '\n'), 0o644)
}

func b64Plain(s string) string {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(b)
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ptrTo[T any](v T) *T { return &v }

// sleepCtx waits d, or returns early when ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
