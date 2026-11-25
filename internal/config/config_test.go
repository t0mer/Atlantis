package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "atlantis.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `
deco:
  host: 192.0.2.10
  email: owner@example.com
secret_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
`

func TestDefaultsAndYAML(t *testing.T) {
	c, err := Load(writeYAML(t, minimal), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Deco.Transport != "ssh" || c.Poll.Fast != 30*time.Second || c.Poll.Slow != 5*time.Minute ||
		c.Store.RawRetention != 168*time.Hour || c.Metrics.PerClient != "watched" || c.Server.Listen != ":8080" {
		t.Fatalf("%+v", c)
	}
}

func TestPrecedenceFlagOverEnvOverYAML(t *testing.T) {
	path := writeYAML(t, minimal+"poll:\n  fast: 45s\n")
	t.Setenv("ATLANTIS_POLL_FAST", "50s")
	t.Setenv("ATLANTIS_DECO_PORT", "20001")
	c, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Poll.Fast != 50*time.Second || c.Deco.Port != 20001 {
		t.Fatalf("env must beat YAML: %+v", c.Poll)
	}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.Duration("poll-fast", 0, "")
	fs.Parse([]string{"--poll-fast=1m"})
	if c, err = Load(path, fs); err != nil || c.Poll.Fast != time.Minute {
		t.Fatalf("flag must beat env: %v %+v", err, c)
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	_, err := Load(writeYAML(t, `
secret_key: dGVzdA==
deco: {transport: telnet}
poll: {fast: 2s}
metrics: {per_client: some}
notify: {timezone: Mars/Base, quiet_hours: "25:00-07:00"}
log: {level: loud}
`), nil)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"transport", "poll intervals", "per_client", "timezone", "quiet_hours", "log.level", "ATLANTIS_SECRET_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestQuietHours(t *testing.T) {
	s, e, err := ParseQuietHours("23:00-07:30")
	if err != nil || s != 23*60 || e != 7*60+30 {
		t.Fatal(s, e, err)
	}
	if _, _, err := ParseQuietHours("23:00"); err == nil {
		t.Fatal("missing range accepted")
	}
}

func TestDecoIsOptional(t *testing.T) {
	t.Setenv("ATLANTIS_SECRET_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if _, err := Load(writeYAML(t, "server: {listen: ':8080'}\n"), nil); err != nil {
		t.Fatalf("no Deco must be fine, it is added from the UI: %v", err)
	}
	_, err := Load(writeYAML(t, "deco: {host: 192.0.2.1}\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "deco.email") {
		t.Fatalf("a host without the TP-Link ID must fail: %v", err)
	}
}

func TestSecretKeyIsOptional(t *testing.T) {
	t.Setenv("ATLANTIS_SECRET_KEY", "")
	if _, err := Load(writeYAML(t, "server: {listen: ':8080'}\n"), nil); err != nil {
		t.Fatalf("no key must be fine: a key file is created next to the data: %v", err)
	}
}
