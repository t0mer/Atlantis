package tmpclient_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func newClient(t *testing.T, d *fakedeco.Device) *tmpclient.Client {
	t.Helper()
	dial := func(context.Context, func(string, ...any)) (io.ReadWriteCloser, error) {
		c, s := net.Pipe()
		go d.Serve(s)
		return c, nil
	}
	c := tmpclient.New(dial, tmp.Options{})
	t.Cleanup(func() { c.Logout(context.Background()) })
	return c
}

func TestSetHostWiFiChangesEveryBandAndKeepsOtherSettings(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpWirelessGet, map[string]any{
		"band2_4": map[string]any{
			"host":  map[string]any{"enable": true, "ssid": b64("old"), "password": b64("oldpass123"), "encryption_mode": "psk", "enable_hide_ssid": false},
			"guest": map[string]any{"enable": false, "ssid": b64("guest")},
		},
		"band5_1":     map[string]any{"host": map[string]any{"enable": true, "ssid": b64("old"), "password": b64("oldpass123"), "encryption_mode": "psk"}},
		"config_spec": map[string]any{"ssid": b64("old")},
	})
	c := newClient(t, d)
	ch, err := c.SetHostWiFi(context.Background(), "Test-SSID", "new-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ch.Bands, ",") != "band2_4,band5_1" {
		t.Fatalf("bands %v", ch.Bands)
	}
	var sent struct {
		Params map[string]map[string]map[string]any `json:"params"`
	}
	json.Unmarshal(d.Request(tmp.OpWirelessSet), &sent)
	h := sent.Params["band2_4"]["host"]
	if h["ssid"] != b64("Test-SSID") || h["password"] != b64("new-pass-1") || h["encryption_mode"] != "psk" || h["enable"] != true {
		t.Fatalf("host params wrong: %v", h)
	}
	if _, ok := sent.Params["band2_4"]["guest"]; ok {
		t.Fatal("the guest network must not be part of the change")
	}
	if _, ok := sent.Params["config_spec"]; ok {
		t.Fatal("config_spec must not be sent")
	}
}

func TestSetHostWiFiValidates(t *testing.T) {
	c := newClient(t, fakedeco.New())
	for _, tc := range [][2]string{{"", "longenough"}, {strings.Repeat("x", 33), "longenough"}, {"ok", "short"}, {"ok", strings.Repeat("p", 64)}, {"ok", "pässword-1"}} {
		if _, err := c.SetHostWiFi(context.Background(), tc[0], tc[1]); err == nil {
			t.Errorf("accepted ssid=%q password=%q", tc[0], tc[1])
		}
	}
}

func TestRawRefusesWrites(t *testing.T) {
	c := newClient(t, fakedeco.New())
	if _, err := c.Raw(context.Background(), tmpclient.RawArea, "WIRELESS_SET"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("got %v", err)
	}
}

// Review findings for the write path.
func TestSetHostWiFiToleratesOddReadBack(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpWirelessGet, map[string]any{
		"band2_4":       map[string]any{"host": map[string]any{"enable": true, "ssid": b64("old"), "password": b64("oldpass123")}},
		"band6_2":       "unexpected",
		"config_spec":   []any{1, 2},
		"band_steering": map[string]any{"host": map[string]any{"ssid": b64("must-not-change")}},
	})
	c := newClient(t, d)
	ch, err := c.SetHostWiFi(context.Background(), "new-ssid", "new-pass-1")
	if err != nil {
		t.Fatalf("a successful write was reported as failed: %v", err)
	}
	if len(ch.Bands) != 1 || ch.Bands[0] != "band2_4" {
		t.Fatalf("%v", ch.Bands)
	}
	var sent struct {
		Params map[string]any `json:"params"`
	}
	json.Unmarshal(d.Request(tmp.OpWirelessSet), &sent)
	if _, ok := sent.Params["band_steering"]; ok {
		t.Fatal("a non-band key was rewritten")
	}
}

func TestSetHostWiFiRejectsControlCharsBeforeSending(t *testing.T) {
	d := fakedeco.New()
	c := newClient(t, d)
	if _, err := c.SetHostWiFi(context.Background(), "a\nb", "longenough"); err == nil {
		t.Fatal("control characters accepted")
	}
	if d.Request(tmp.OpWirelessSet) != nil {
		t.Fatal("the write was sent")
	}
}

func TestRequestRefusesWrites(t *testing.T) {
	c := newClient(t, fakedeco.New())
	if _, err := c.Request(context.Background(), tmp.OpWirelessSet, map[string]any{}); err == nil {
		t.Fatal("Request sent a write")
	}
}

func TestLoginErrorsAreNotUnsupported(t *testing.T) {
	boom := errors.New("ssh: unable to authenticate")
	c := tmpclient.New(func(context.Context, func(string, ...any)) (io.ReadWriteCloser, error) { return nil, boom }, tmp.Options{})
	if _, err := c.WAN(context.Background()); !errors.Is(err, boom) || errors.Is(err, deco.ErrUnsupported) {
		t.Fatalf("WAN: %v", err)
	}
	if _, err := c.Performance(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Performance: %v", err)
	}
}
