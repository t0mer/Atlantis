package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
	"github.com/t0mer/atlantis/internal/tplinkcloud"
	"github.com/t0mer/atlantis/internal/tplinkcloud/fakecloud"
)

func cloudFakes(t *testing.T, mfa bool) (*fakecloud.Fake, *fakedeco.Device) {
	t.Helper()
	fc := fakecloud.New()
	t.Cleanup(fc.Close)
	fc.SetRequireMFA(mfa)
	dev := fakedeco.New()
	dev.SetResult(tmp.OpWirelessGet, map[string]any{"band2_4": map[string]any{"host": map[string]any{
		"enable": true, "ssid": "SG9tZQ==", "password": "dG9wLXNlY3JldC13aWZp"}}})
	dev.SetResult(tmp.OpClientListGet, map[string]any{"client_list": []any{map[string]any{
		"mac": "11-22-33-44-55-66", "ip": "192.168.68.77", "name": "S2lkcyBpUGFk", "online": true}}})
	fc.SetPassthrough(dev.Relay)
	fc.SetDevices([]fakecloud.Device{
		{ID: "master-1", Master: true, Online: true, GroupID: "g-home", GroupName: "Home"},
		{ID: "master-2", Master: true, GroupID: "g-cabin", GroupName: "Cabin"},
	})
	return fc, dev
}

func cloudOpts(t *testing.T, fc *fakecloud.Fake, stdin string) (cloudProbeOptions, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return cloudProbeOptions{
		email:        fakecloud.Email,
		password:     fakecloud.Password,
		keys:         tplinkcloud.AppKeys{AccessKey: fakecloud.AccessKey, Secret: fakecloud.Secret},
		terminalFile: filepath.Join(t.TempDir(), "terminal-id"),
		dumpDir:      filepath.Join(t.TempDir(), "fixtures"),
		bootstrapURL: fc.URL, allowHTTP: true,
		in: strings.NewReader(stdin), out: &out, errOut: &errOut,
	}, &out, &errOut
}

func TestCloudProbeListsSites(t *testing.T) {
	fc, _ := cloudFakes(t, false)
	o, out, _ := cloudOpts(t, fc, "")
	if err := runCloudProbe(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"g-home", "Home", "online", "g-cabin", "Cabin", "offline"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("site list missing %q:\n%s", want, out)
		}
	}
}

func TestCloudProbeMFAPromptsForCode(t *testing.T) {
	fc, _ := cloudFakes(t, true)
	o, out, errOut := cloudOpts(t, fc, fakecloud.MFACode+"\n")
	if err := runCloudProbe(context.Background(), o); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if fc.CodesSent() != 1 || !strings.Contains(errOut.String(), "o***@example.com") || !strings.Contains(out.String(), "g-home") {
		t.Fatalf("MFA flow wrong: sent=%d\n%s\n%s", fc.CodesSent(), errOut, out)
	}
}

func TestCloudProbeSiteDumpsWithoutLeaks(t *testing.T) {
	fc, dev := cloudFakes(t, false)
	o, out, errOut := cloudOpts(t, fc, "")
	o.site, o.dump = "g-home", true
	err := runCloudProbe(context.Background(), o)
	// FW_LATEST_GET and friends have no canned reply, so some steps fail; the
	// probe must still print, dump and disconnect.
	if err == nil || !strings.Contains(err.Error(), "FW_LATEST_GET") {
		t.Fatalf("expected the unsupported steps to be reported, got %v", err)
	}
	if !dev.Freed() {
		t.Error("session not closed with TOKEN_FREE")
	}
	if strings.Contains(out.String(), "dG9wLXNlY3JldC13aWZp") {
		t.Fatal("Wi-Fi password printed")
	}
	if !strings.Contains(out.String(), `"device_id": "dev-1"`) {
		t.Fatalf("device list missing:\n%s", out)
	}
	files, _ := filepath.Glob(filepath.Join(o.dumpDir, "*.json"))
	if len(files) < 3 {
		t.Fatalf("want fixtures, got %v\n%s", files, errOut)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, leak := range []string{"dG9wLXNlY3JldC13aWZp", "11-22-33-44-55-66", "192.168.68.77", "S2lkcyBpUGFk", "AA-BB-CC-DD-EE-01", fakecloud.Email} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s leaks %q", filepath.Base(f), leak)
			}
		}
	}
}

func TestCloudProbeUnknownSite(t *testing.T) {
	fc, _ := cloudFakes(t, false)
	o, _, _ := cloudOpts(t, fc, "")
	o.site = "nope"
	if err := runCloudProbe(context.Background(), o); err == nil || !strings.Contains(err.Error(), "no site") {
		t.Fatalf("got %v", err)
	}
}

func TestCloudProbeNeedsKeys(t *testing.T) {
	fc, _ := cloudFakes(t, false)
	o, _, _ := cloudOpts(t, fc, "")
	o.keys = tplinkcloud.AppKeys{}
	if err := runCloudProbe(context.Background(), o); err == nil || !strings.Contains(err.Error(), "ATLANTIS_CLOUD_ACCESS_KEY") {
		t.Fatalf("got %v", err)
	}
}

func TestTerminalIDIsStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "terminal-id")
	a, err := loadTerminalID(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := loadTerminalID(p)
	st, _ := os.Stat(p)
	if a != b || st.Mode().Perm() != 0o600 {
		t.Fatalf("unstable or wrong perms: %s %s %v", a, b, st.Mode())
	}
}

func TestDefaultCAFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got := defaultCAFile(); got != "" {
		t.Fatalf("no file yet, got %q", got)
	}
	p := filepath.Join(dir, "atlantis", caFileName)
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("x"), 0o600)
	if got := defaultCAFile(); got != p {
		t.Fatalf("got %q, want %q", got, p)
	}
}
