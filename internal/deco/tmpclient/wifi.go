package tmpclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
)

// HostWiFiChange describes a main-network SSID/password change.
type HostWiFiChange struct {
	Bands []string // bands that were changed
}

// SetHostWiFi sets the main (host) network's SSID and password on every
// band that has one, keeping each band's other host settings as the device
// reports them, then reads the settings back to confirm (the app sends only
// the changed networks: MeshWirelessRepository H0/I0). Every client on the
// main network is disconnected until it reconnects with the new details.
func (c *Client) SetHostWiFi(ctx context.Context, ssid, password string) (*HostWiFiChange, error) {
	if err := validateHostWiFi(ssid, password); err != nil {
		return nil, err
	}
	cur, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		return nil, fmt.Errorf("reading current Wi-Fi settings: %w", err)
	}
	var bands map[string]json.RawMessage
	if err := json.Unmarshal(cur, &bands); err != nil {
		return nil, fmt.Errorf("parsing Wi-Fi settings: %w", err)
	}
	params := map[string]any{}
	var changed []string
	for band, raw := range bands {
		if !strings.HasPrefix(band, "band") {
			continue // config_spec and similar
		}
		var b struct {
			Host map[string]json.RawMessage `json:"host"`
		}
		if json.Unmarshal(raw, &b) != nil || b.Host == nil {
			continue
		}
		host := map[string]any{}
		for k, v := range b.Host {
			host[k] = v
		}
		host["ssid"] = base64.StdEncoding.EncodeToString([]byte(ssid))
		host["password"] = base64.StdEncoding.EncodeToString([]byte(password))
		params[band] = map[string]any{"host": host}
		changed = append(changed, band)
	}
	if len(changed) == 0 {
		return nil, errors.New("tmpclient: the device reports no main Wi-Fi network to change")
	}
	sort.Strings(changed)
	if _, err := c.Request(ctx, tmp.OpWirelessSet, params); err != nil {
		return nil, fmt.Errorf("setting Wi-Fi: %w", err)
	}

	after, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		return nil, fmt.Errorf("the change was sent, but reading it back failed: %w", err)
	}
	var check map[string]struct {
		Host struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		} `json:"host"`
	}
	if err := json.Unmarshal(after, &check); err != nil {
		return nil, fmt.Errorf("the change was sent, but the read-back is unreadable: %w", err)
	}
	for _, band := range changed {
		h := check[band].Host
		if deco.DecodeName(h.SSID) != ssid || deco.DecodeName(h.Password) != password {
			return nil, fmt.Errorf("the device accepted the change but %s does not show it yet", band)
		}
	}
	return &HostWiFiChange{Bands: changed}, nil
}

// validateHostWiFi applies WPA2 limits: SSID 1-32 bytes, passphrase 8-63
// printable ASCII characters.
func validateHostWiFi(ssid, password string) error {
	if n := len(ssid); n < 1 || n > 32 || !utf8.ValidString(ssid) {
		return fmt.Errorf("SSID must be 1-32 bytes of text (got %d bytes)", n)
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
