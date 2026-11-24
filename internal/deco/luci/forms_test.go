package luci

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeName(t *testing.T) {
	hebrew := base64.StdEncoding.EncodeToString([]byte("שלום"))
	cases := []struct{ in, want string }{
		{"", ""},
		{hebrew, "שלום"},
		{"<Error Decoding Laptop>", "Laptop"},
		{"plain-not-base64!", "plain-not-base64!"},
	}
	for _, tc := range cases {
		if got := decodeName(tc.in); got != tc.want {
			t.Errorf("decodeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeMACRoundTrip(t *testing.T) {
	if got := normalizeMAC("AA-BB-CC-DD-EE-FF"); got != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("normalizeMAC = %q", got)
	}
	if got := wireMAC("aa:bb:cc:dd:ee:ff"); got != "AA-BB-CC-DD-EE-FF" {
		t.Fatalf("wireMAC = %q", got)
	}
}

func TestReadFormsMapToModel(t *testing.T) {
	f := newFake(t)
	c, err := New(f.Plain.URL, "secret", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	devs, err := c.Devices(ctx)
	if err != nil || len(devs) != 1 {
		t.Fatalf("Devices: %v (%d)", err, len(devs))
	}
	if devs[0].MAC != "aa:bb:cc:dd:ee:01" || devs[0].Nickname != "שלום" || devs[0].Role != "master" {
		t.Fatalf("device mapping wrong: %+v", devs[0])
	}

	clients, err := c.Clients(ctx, "AA-BB-CC-DD-EE-01")
	if err != nil || len(clients) != 2 {
		t.Fatalf("Clients: %v (%d)", err, len(clients))
	}
	if clients[0].Name != "מכשיר" || !clients[0].Online || clients[0].MAC != "11:22:33:44:55:66" {
		t.Fatalf("client[0] wrong: %+v", clients[0])
	}
	if clients[1].Name != "Laptop" {
		t.Fatalf("legacy error-wrapped name not unwrapped: %q", clients[1].Name)
	}

	wan, err := c.WAN(ctx)
	if err != nil || wan.IP != "203.0.113.7" || wan.LANIP != "192.168.68.1" {
		t.Fatalf("WAN: %v %+v", err, wan)
	}

	perf, err := c.Performance(ctx)
	if err != nil || perf.CPUUsage != 0.12 || perf.MemUsage != 0.48 {
		t.Fatalf("Performance: %v %+v", err, perf)
	}

	wl, err := c.Wireless(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h := wl.Bands["band2_4"]["host"]; !h.Enable || h.SSID != "הבית" {
		t.Fatalf("wireless band2_4/host wrong: %+v", h)
	}
	if g := wl.Bands["band5_1"]["guest"]; g.Enable {
		t.Fatalf("guest should be disabled: %+v", g)
	}
}

func TestClientsDefaultsToAllNodes(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.Plain.URL, "secret", Options{})
	if _, err := c.Clients(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestRawReturnsDecryptedResult(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.Plain.URL, "secret", Options{})
	raw, err := c.Raw(context.Background(), "network", "performance")
	if err != nil || !strings.Contains(string(raw), "cpu_usage") {
		t.Fatalf("Raw: %v %s", err, raw)
	}
}

func TestRawRedactsSecrets(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.Plain.URL, "secret", Options{})
	raw, err := c.Raw(context.Background(), "wireless", "wlan")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") {
		t.Fatalf("Raw leaked a Wi-Fi password: %s", raw)
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("expected a redaction marker: %s", raw)
	}
}
