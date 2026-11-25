package tmpclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
)

// verifiedWrites are the Wi-Fi changes proven on a real Deco (an M5) with
// `atlantis ssh wifi-probe`; SetNetwork refuses anything else. Add an entry
// only after a probe of that exact network and field passes on hardware.
// Each entry is backed by a passing record in testdata/m5 (wifi_probe_*.json;
// TestVerifiedWritesHaveEvidence checks this), except the main network's
// SSID and password, verified with `atlantis ssh set-wifi` before the probe
// existed.
var verifiedWrites = map[string][]string{
	"host":  {deco.FieldSSID, deco.FieldPassword},
	"guest": {deco.FieldEnable, deco.FieldSSID, deco.FieldPassword, deco.FieldEncryption},
	"iot":   {deco.FieldEnable, deco.FieldSSID, deco.FieldPassword},
}

// verifiedEncryption are the security values proven per network; a field
// being verified is not enough, each value is sent only once proven.
var verifiedEncryption = map[string][]string{
	"guest": {"none", "wpa2"},
}

// Editable reports the verified fields per network.
func (c *Client) Editable() map[string][]string {
	out := make(map[string][]string, len(verifiedWrites))
	for k, v := range verifiedWrites {
		out[k] = slices.Clone(v)
	}
	return out
}

// Encryption modes the Deco reports (WIRELESS_GET encryption_mode).
var encryptionModes = []string{"none", "psk", "wpa2", "wpa2+wpa", "wpa3", "wpa2+wpa3"}

// networks a patch may target; the device's keys.
var patchNetworks = []string{"host", "guest", "iot"}

// SetNetwork changes one Wi-Fi network: only fields and values verified on
// hardware, on every band at once (the shape the probes proved).
func (c *Client) SetNetwork(ctx context.Context, p deco.WirelessPatch) ([]string, error) {
	for _, f := range p.Fields() {
		if !slices.Contains(verifiedWrites[p.Network], f) {
			return nil, fmt.Errorf("%w: %s %s", deco.ErrNotVerified, networkName(p.Network), f)
		}
	}
	if p.Encryption != nil && !slices.Contains(verifiedEncryption[p.Network], *p.Encryption) {
		return nil, fmt.Errorf("%w: %s security %q", deco.ErrNotVerified, networkName(p.Network), *p.Encryption)
	}
	if len(p.Bands) > 0 {
		return nil, fmt.Errorf("%w: changing a single band", deco.ErrNotVerified)
	}
	return c.setNetwork(ctx, p)
}

// ProbeNetwork is SetNetwork without the verified-writes check. It exists
// for `atlantis ssh wifi-probe`, which proves a write on hardware before it
// is added to verifiedWrites; nothing else may call it.
func (c *Client) ProbeNetwork(ctx context.Context, p deco.WirelessPatch) ([]string, error) {
	return c.setNetwork(ctx, p)
}

// SetHostWiFi sets the main network's SSID and password on every band.
func (c *Client) SetHostWiFi(ctx context.Context, ssid, password string) ([]string, error) {
	return c.SetNetwork(ctx, deco.WirelessPatch{Network: "host", SSID: &ssid, Password: &password})
}

// setNetwork reads the current settings, changes only the patched fields of
// the one network on the chosen bands (keeping everything else as the
// device reports it), sends just that network (the app sends only changed
// networks: MeshWirelessRepository H0/I0), and reads back to confirm.
// Devices on the network disconnect until they reconnect.
func (c *Client) setNetwork(ctx context.Context, p deco.WirelessPatch) ([]string, error) {
	if err := validatePatch(p); err != nil {
		return nil, err
	}
	cur, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		return nil, fmt.Errorf("reading current Wi-Fi settings: %w", err)
	}
	top, ok := deco.ParseObj(cur)
	if !ok {
		return nil, errors.New("tmpclient: Wi-Fi settings are not an object")
	}
	bands := p.Bands
	if len(bands) == 0 {
		for _, b := range deco.Bands {
			if _, ok := deco.ParseObj(top.Obj(b)[p.Network]); ok {
				bands = append(bands, b)
			}
		}
	}
	if len(bands) == 0 {
		return nil, fmt.Errorf("tmpclient: the device reports no %s Wi-Fi network", networkName(p.Network))
	}
	params, orig := map[string]any{}, map[string]any{}
	for _, band := range bands {
		net, ok := deco.ParseObj(top.Obj(band)[p.Network])
		if !ok {
			return nil, deco.Invalid("%s has no %s network", band, networkName(p.Network))
		}
		out, err := applyPatch(net, p)
		if err != nil {
			return nil, deco.Invalid("%s: %s", band, err.Error())
		}
		params[band] = map[string]any{p.Network: out}
		orig[band] = map[string]any{p.Network: net}
	}
	if _, err := c.request(ctx, tmp.OpWirelessSet, params); err != nil {
		return nil, fmt.Errorf("setting Wi-Fi: %w", err)
	}

	// Undoing a change must not depend on the caller still waiting.
	undo := func() error {
		_, err := c.request(context.WithoutCancel(ctx), tmp.OpWirelessSet, orig)
		return err
	}
	after, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		if rerr := undo(); rerr != nil {
			return nil, fmt.Errorf("the change was sent, but reading it back failed (%w), and restoring the previous settings failed: %v", err, rerr)
		}
		return nil, fmt.Errorf("the change was sent, but reading it back failed (%w); the previous settings were restored", err)
	}
	check, _ := deco.ParseObj(after)
	for _, band := range bands {
		if err := confirm(check.Obj(band).Obj(p.Network), p); err != nil {
			// Half-applied or rejected: put the previous settings back.
			if rerr := undo(); rerr != nil {
				return nil, fmt.Errorf("the device accepted the change but %s %s, and restoring the previous settings failed: %v", band, err, rerr)
			}
			return nil, fmt.Errorf("the device accepted the change but %s %s; the previous settings were restored", band, err)
		}
	}
	sort.Strings(bands)
	return bands, nil
}

// applyPatch returns a copy of the device's network object with the patch
// applied. A field the device does not report for this network is an error,
// so Atlantis never invents keys.
func applyPatch(net deco.Obj, p deco.WirelessPatch) (map[string]any, error) {
	out := make(map[string]any, len(net))
	for k, v := range net {
		out[k] = v // keep the device's other settings verbatim
	}
	has := func(k string) error {
		if _, ok := net[k]; !ok {
			return fmt.Errorf("the device does not report %q for the %s network", k, networkName(p.Network))
		}
		return nil
	}
	if p.Enable != nil {
		if err := has("enable"); err != nil {
			return nil, err
		}
		out["enable"] = *p.Enable
	}
	if p.SSID != nil {
		out["ssid"] = base64.StdEncoding.EncodeToString([]byte(*p.SSID))
	}
	if p.Password != nil {
		out["password"] = base64.StdEncoding.EncodeToString([]byte(*p.Password))
	}
	if p.Encryption != nil {
		if err := has("encryption_mode"); err != nil {
			return nil, err
		}
		if *p.Encryption != "none" && p.Password == nil && b64Text(net.Str("password")) == "" {
			return nil, errors.New("a password is required to secure an open network")
		}
		out["encryption_mode"] = *p.Encryption
	}
	if p.Hidden != nil {
		if err := has("enable_hide_ssid"); err != nil {
			return nil, err
		}
		out["enable_hide_ssid"] = *p.Hidden
	}
	return out, nil
}

// confirm checks the read-back network against the patch.
func confirm(n deco.Obj, p deco.WirelessPatch) error {
	switch {
	case p.Enable != nil && n.Bool("enable") != *p.Enable:
		return errors.New("does not show the network turned on or off yet")
	case p.SSID != nil && b64Text(n.Str("ssid")) != *p.SSID:
		return errors.New("does not show the new name yet")
	case p.Password != nil && b64Text(n.Str("password")) != *p.Password:
		return errors.New("does not show the new password yet")
	case p.Encryption != nil && n.Str("encryption_mode") != *p.Encryption:
		return errors.New("does not show the new security yet")
	case p.Hidden != nil && n.Bool("enable_hide_ssid") != *p.Hidden:
		return errors.New("does not show the network hidden or shown yet")
	}
	return nil
}

// ValidatePatch checks a patch without sending it (the probe validates its
// restore before changing anything).
func ValidatePatch(p deco.WirelessPatch) error { return validatePatch(p) }

func validatePatch(p deco.WirelessPatch) error {
	if !slices.Contains(patchNetworks, p.Network) {
		return deco.Invalid("unknown Wi-Fi network %q", p.Network)
	}
	if len(p.Fields()) == 0 {
		return deco.Invalid("nothing to change")
	}
	if p.Network == "host" && p.Enable != nil {
		return deco.Invalid("the main network cannot be turned off")
	}
	for _, b := range p.Bands {
		if !deco.IsBand(b) {
			return deco.Invalid("unknown band %q", b)
		}
	}
	if p.SSID != nil {
		if err := validateSSID(*p.SSID); err != nil {
			return deco.Invalid("%s", err.Error())
		}
	}
	if p.Password != nil {
		if err := validatePassphrase(*p.Password); err != nil {
			return deco.Invalid("%s", err.Error())
		}
	}
	if p.Encryption != nil && !slices.Contains(encryptionModes, *p.Encryption) {
		return deco.Invalid("unknown security %q", *p.Encryption)
	}
	return nil
}

func networkName(n string) string {
	switch n {
	case "host":
		return "main"
	case "iot":
		return "IoT"
	}
	return n
}

// validateSSID: 1-32 bytes of printable text.
func validateSSID(ssid string) error {
	if n := len(ssid); n < 1 || n > 32 || !utf8.ValidString(ssid) {
		return fmt.Errorf("SSID must be 1-32 bytes of text (got %d bytes)", n)
	}
	for _, r := range ssid {
		if !unicode.IsPrint(r) {
			return errors.New("SSID must not contain control characters")
		}
	}
	return nil
}

// validatePassphrase: WPA2 limits, 8-63 printable ASCII characters.
func validatePassphrase(password string) error {
	if n := len(password); n < 8 || n > 63 {
		return fmt.Errorf("Wi-Fi password must be 8-63 characters (got %d)", n)
	}
	for _, r := range password {
		if r < 0x20 || r > 0x7e {
			return errors.New("Wi-Fi password must be printable ASCII")
		}
	}
	return nil
}

// b64Text decodes a base64 field as raw bytes (no printable check, unlike
// deco.DecodeName), returning the value as given when it is not base64.
func b64Text(s string) string {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(b)
	}
	return s
}
