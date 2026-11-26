package config

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range append([]string{"ATLANTIS_SERVER_LISTEN", "ATLANTIS_STORE_PATH", "ATLANTIS_SECRET_KEY"}, obsolete...) {
		t.Setenv(k, "") // restores the old value after the test
		os.Unsetenv(k)
	}
}

func TestNothingIsRequired(t *testing.T) {
	clearEnv(t)
	c, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != DefaultListen || c.StorePath != DefaultStorePath || c.SecretKey != "" {
		t.Fatalf("%+v", c)
	}
}

func TestFlagOverEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("ATLANTIS_SERVER_LISTEN", "127.0.0.1:9000")
	t.Setenv("ATLANTIS_STORE_PATH", "/srv/a.db")
	c, err := Load(nil)
	if err != nil || c.Listen != "127.0.0.1:9000" || c.StorePath != "/srv/a.db" {
		t.Fatalf("env: %v %+v", err, c)
	}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.String("server-listen", DefaultListen, "")
	fs.String("store-path", DefaultStorePath, "")
	if err := fs.Parse([]string{"--server-listen=:9100"}); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(fs); err != nil || c.Listen != ":9100" || c.StorePath != "/srv/a.db" {
		t.Fatalf("a flag that is set beats env; an unset one keeps it: %v %+v", err, c)
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	clearEnv(t)
	t.Setenv("ATLANTIS_SERVER_LISTEN", "nope")
	t.Setenv("ATLANTIS_SECRET_KEY", "dGVzdA==")
	_, err := Load(nil)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"listen address", "ATLANTIS_SECRET_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestObsoleteVariablesAreReported(t *testing.T) {
	clearEnv(t)
	if got := Obsolete(); len(got) != 0 {
		t.Fatalf("nothing set: %v", got)
	}
	t.Setenv("ATLANTIS_DECO_HOST", "192.0.2.1")
	t.Setenv("ATLANTIS_CONFIG", "/etc/atlantis.yaml")
	got := Obsolete()
	if !slices.Contains(got, "ATLANTIS_DECO_HOST") || !slices.Contains(got, "ATLANTIS_CONFIG") || len(got) != 2 {
		t.Fatalf("%v", got)
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
