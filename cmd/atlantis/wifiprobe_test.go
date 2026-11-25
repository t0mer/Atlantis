package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
)

func probeClient(t *testing.T, wireless map[string]any) (*tmpclient.Client, *fakedeco.Device) {
	t.Helper()
	d := fakedeco.New()
	d.SetResult(tmp.OpWirelessGet, wireless)
	c := tmpclient.New(func(context.Context, func(string, ...any)) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go d.Serve(b)
		return a, nil
	}, tmp.Options{})
	t.Cleanup(func() { c.Logout(context.Background()) })
	return c, d
}

func enc(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestWiFiProbeChangesAndRestores(t *testing.T) {
	probeSettle = 0
	guest := func() map[string]any {
		return map[string]any{"enable": false, "ssid": enc("Real Guest"), "password": enc("kept-guest-pw"), "encryption_mode": "none", "access_duration": "14400"}
	}
	for _, field := range []string{"enable", "ssid", "encryption"} {
		t.Run(field, func(t *testing.T) {
			c, d := probeClient(t, map[string]any{"band2_4": map[string]any{"guest": guest()}, "band5_1": map[string]any{"guest": guest()}})
			dir := t.TempDir()
			var out bytes.Buffer
			if err := wifiProbe(context.Background(), c, "guest", field, nil, dir, sshProbeOptions{out: &out, errOut: &out}); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			// The Deco is back where it started.
			var sent struct {
				Params map[string]map[string]map[string]any `json:"params"`
			}
			json.Unmarshal(d.Request(tmp.OpWirelessSet), &sent)
			g := sent.Params["band2_4"]["guest"]
			if g["enable"] != false || g["ssid"] != enc("Real Guest") || g["encryption_mode"] != "none" {
				t.Fatalf("not restored: %v", g)
			}
			b, err := os.ReadFile(filepath.Join(dir, "wifi_probe_guest_"+field+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte("Real Guest")) || bytes.Contains(b, []byte(enc("Real Guest"))) || bytes.Contains(b, []byte("atlantis-probe-")) {
				t.Fatalf("the record must not carry names or passwords:\n%s", b)
			}
			var rec probeRecord
			json.Unmarshal(b, &rec)
			if rec.Result != "pass" {
				t.Fatalf("%+v", rec)
			}
		})
	}
}

func TestWiFiProbeRefusesRiskyChanges(t *testing.T) {
	probeSettle = 0
	on := map[string]any{"enable": true, "ssid": enc("x"), "password": enc("iot-pass-1"), "encryption_mode": "wpa2"}
	c, d := probeClient(t, map[string]any{"band2_4": map[string]any{"iot": on, "host": on}})
	var out bytes.Buffer
	o := sshProbeOptions{out: &out, errOut: &out}
	for _, tc := range [][2]string{{"iot", "enable"}, {"host", "enable"}, {"host", "encryption"}} {
		if err := wifiProbe(context.Background(), c, tc[0], tc[1], nil, t.TempDir(), o); err == nil {
			t.Errorf("%v must be refused", tc)
		}
	}
	if d.Request(tmp.OpWirelessSet) != nil {
		t.Fatal("a refused probe sent a write")
	}
}

// A probe whose undo would be invalid (here a 64-character hex PSK, longer
// than a passphrase) changes nothing.
func TestWiFiProbeChecksTheRestoreFirst(t *testing.T) {
	probeSettle = 0
	psk := strings.Repeat("ab", 32)
	g := map[string]any{"enable": true, "ssid": enc("g"), "password": enc(psk), "encryption_mode": "wpa2"}
	c, d := probeClient(t, map[string]any{"band2_4": map[string]any{"guest": g}})
	var out bytes.Buffer
	err := wifiProbe(context.Background(), c, "guest", "password", nil, t.TempDir(), sshProbeOptions{out: &out, errOut: &out})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("got %v", err)
	}
	if d.Request(tmp.OpWirelessSet) != nil {
		t.Fatal("a change that could not be undone was sent")
	}
}
