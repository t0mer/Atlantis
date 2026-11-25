package tmpclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
)

// HostWiFiChange describes a main-network SSID/password change.
type HostWiFiChange struct {
	Bands []string // bands that were changed
}

// SetHostWiFi sets the main (host) network's SSID and password on every
// known band that has one, keeping each band's other host settings as the
// device reports them, then reads the settings back to confirm (the app
// sends only the changed networks: MeshWirelessRepository H0/I0). Every
// client on the main network is disconnected until it reconnects.
func (c *Client) SetHostWiFi(ctx context.Context, ssid, password string) (*HostWiFiChange, error) {
	if err := validateHostWiFi(ssid, password); err != nil {
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
	ssidB64 := base64.StdEncoding.EncodeToString([]byte(ssid))
	passB64 := base64.StdEncoding.EncodeToString([]byte(password))
	params := map[string]any{}
	var changed []string
	for _, band := range deco.Bands {
		host, ok := deco.ParseObj(top.Obj(band)["host"])
		if !ok {
			continue
		}
		out := make(map[string]any, len(host))
		for k, v := range host {
			out[k] = v // keep the device's other host settings verbatim
		}
		out["ssid"], out["password"] = ssidB64, passB64
		params[band] = map[string]any{"host": out}
		changed = append(changed, band)
	}
	if len(changed) == 0 {
		return nil, errors.New("tmpclient: the device reports no main Wi-Fi network to change")
	}
	if _, err := c.request(ctx, tmp.OpWirelessSet, params); err != nil {
		return nil, fmt.Errorf("setting Wi-Fi: %w", err)
	}

	after, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		return nil, fmt.Errorf("the change was sent, but reading it back failed: %w", err)
	}
	check, _ := deco.ParseObj(after)
	for _, band := range changed {
		h := check.Obj(band).Obj("host")
		if b64Text(h.Str("ssid")) != ssid || b64Text(h.Str("password")) != password {
			return nil, fmt.Errorf("the device accepted the change but %s does not show it yet", band)
		}
	}
	sort.Strings(changed)
	return &HostWiFiChange{Bands: changed}, nil
}

// validateHostWiFi applies WPA2 limits: SSID 1-32 bytes of printable text,
// passphrase 8-63 printable ASCII characters.
func validateHostWiFi(ssid, password string) error {
	if n := len(ssid); n < 1 || n > 32 || !utf8.ValidString(ssid) {
		return fmt.Errorf("SSID must be 1-32 bytes of text (got %d bytes)", n)
	}
	for _, r := range ssid {
		if !unicode.IsPrint(r) {
			return errors.New("SSID must not contain control characters")
		}
	}
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
