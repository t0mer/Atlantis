package tmpclient_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if strings.Join(ch, ",") != "band2_4,band5_1" {
		t.Fatalf("bands %v", ch)
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
	if len(ch) != 1 || ch[0] != "band2_4" {
		t.Fatalf("%v", ch)
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

// Review finding: a session-limit bye received while idle must be reported
// on the next call, not hidden by an immediate reconnect.
func TestIdleByeIsReported(t *testing.T) {
	d := fakedeco.New()
	d.ByeAfter(tmp.OpDeviceListGet)
	dials := 0
	c := tmpclient.New(func(context.Context, func(string, ...any)) (io.ReadWriteCloser, error) {
		dials++
		cl, s := net.Pipe()
		go d.Serve(s)
		return cl, nil
	}, tmp.Options{})
	defer c.Logout(context.Background())
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // the bye arrives while idle
	_, err := c.Devices(context.Background())
	if !errors.Is(err, deco.ErrOtherAdmin) || dials != 1 {
		t.Fatalf("want ErrOtherAdmin without reconnecting, got %v (dials=%d)", err, dials)
	}
}

// m5Wireless loads the recorded (sanitised) M5 WIRELESS_GET result.
func m5Wireless(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/m5/wireless_get.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if r, ok := v["result"].(map[string]any); ok {
		return r
	}
	return v
}

func sentParams(t *testing.T, d *fakedeco.Device) map[string]map[string]map[string]any {
	t.Helper()
	var sent struct {
		Params map[string]map[string]map[string]any `json:"params"`
	}
	json.Unmarshal(d.Request(tmp.OpWirelessSet), &sent)
	return sent.Params
}

func ptr[T any](v T) *T { return &v }

func TestSetNetworkRefusesUnverifiedWrites(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpWirelessGet, m5Wireless(t))
	c := newClient(t, d)
	for _, p := range []deco.WirelessPatch{
		{Network: "iot", Enable: ptr(true)},
		{Network: "iot", SSID: ptr("x-iot")},
		{Network: "host", Hidden: ptr(true)},
		{Network: "host", SSID: ptr("ok"), Encryption: ptr("wpa3")},
	} {
		if _, err := c.SetNetwork(context.Background(), p); !errors.Is(err, deco.ErrNotVerified) {
			t.Errorf("%+v: got %v", p.Fields(), err)
		}
	}
	if d.Request(tmp.OpWirelessSet) != nil {
		t.Fatal("an unverified write was sent")
	}
	if ed := c.Editable(); len(ed["host"]) != 2 || len(ed["iot"]) != 0 {
		t.Fatalf("editable %v", ed)
	}
}

func TestProbeNetworkSendsOnlyThatNetworkAndBand(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpWirelessGet, m5Wireless(t))
	c := newClient(t, d)
	bands, err := c.ProbeNetwork(context.Background(), deco.WirelessPatch{Network: "guest", Bands: []string{"band5_1"}, Enable: ptr(true)})
	if err != nil || len(bands) != 1 || bands[0] != "band5_1" {
		t.Fatalf("%v %v", bands, err)
	}
	sent := sentParams(t, d)
	if len(sent) != 1 || len(sent["band5_1"]) != 1 {
		t.Fatalf("only band5_1 guest may be sent: %v", sent)
	}
	g := sent["band5_1"]["guest"]
	if g["enable"] != true || g["access_duration"] != "14400" || g["host_isolation"] != true {
		t.Fatalf("the guest network's other settings must be kept: %v", g)
	}
}

func TestProbeNetworkSecuresAnOpenNetwork(t *testing.T) {
	d := fakedeco.New()
	w := m5Wireless(t)
	for _, b := range []string{"band2_4", "band5_1"} {
		w[b].(map[string]any)["guest"].(map[string]any)["password"] = ""
	}
	d.SetResult(tmp.OpWirelessGet, w)
	c := newClient(t, d)
	ctx := context.Background()
	if _, err := c.ProbeNetwork(ctx, deco.WirelessPatch{Network: "guest", Encryption: ptr("wpa2")}); !errors.Is(err, deco.ErrInvalid) {
		t.Fatalf("securing without a password: %v", err)
	}
	if _, err := c.ProbeNetwork(ctx, deco.WirelessPatch{Network: "guest", Encryption: ptr("wpa2"), Password: ptr("guest-pass-1")}); err != nil {
		t.Fatal(err)
	}
	g := sentParams(t, d)["band2_4"]["guest"]
	if g["encryption_mode"] != "wpa2" || g["password"] != b64("guest-pass-1") {
		t.Fatalf("%v", g)
	}
}

func TestPatchValidation(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpWirelessGet, m5Wireless(t))
	c := newClient(t, d)
	for _, p := range []deco.WirelessPatch{
		{Network: "host", Enable: ptr(false)}, // the main network stays on
		{Network: "guest"},                    // nothing to change
		{Network: "mlo", Enable: ptr(true)},   // read-only
		{Network: "guest", Bands: []string{"band9"}, Enable: ptr(true)},
		{Network: "guest", Hidden: ptr(true)}, // the M5 reports no enable_hide_ssid for guest
		{Network: "guest", Encryption: ptr("rot13")},
		{Network: "guest", SSID: ptr(strings.Repeat("x", 33))},
		{Network: "guest", Password: ptr("short")},
	} {
		if _, err := c.ProbeNetwork(context.Background(), p); !errors.Is(err, deco.ErrInvalid) {
			t.Errorf("%+v: got %v", p, err)
		}
	}
	if d.Request(tmp.OpWirelessSet) != nil {
		t.Fatal("an invalid write was sent")
	}
}

// Every verified write must be backed by a passing hardware probe record.
func TestVerifiedWritesHaveEvidence(t *testing.T) {
	files, _ := filepath.Glob("testdata/m5/wifi_probe_*.json")
	proven := map[string]bool{"host/" + deco.FieldSSID: true, "host/" + deco.FieldPassword: true} // via ssh set-wifi
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var rec struct {
			Network string `json:"network"`
			Result  string `json:"result"`
			Change  struct {
				Patch []string `json:"patch"`
			} `json:"change"`
			Restore struct {
				Patch []string `json:"patch"`
			} `json:"restore"`
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if rec.Result != "pass" {
			continue
		}
		for _, fl := range append(rec.Change.Patch, rec.Restore.Patch...) {
			proven[rec.Network+"/"+fl] = true
		}
	}
	c := newClient(t, fakedeco.New())
	for net, fields := range c.Editable() {
		for _, f := range fields {
			if !proven[net+"/"+f] {
				t.Errorf("%s %s is marked verified but no passing probe record shows it", net, f)
			}
		}
	}
}
