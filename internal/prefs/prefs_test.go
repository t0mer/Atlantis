package prefs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

func newPrefs(t *testing.T) (*Prefs, *store.Store, *secret.Box) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secret.New(secret.NewKey())
	return New(st, box), st, box
}

func ptr[T any](v T) *T { return &v }

func TestDefaultsApplyUntilSaved(t *testing.T) {
	p, _, _ := newPrefs(t)
	if err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := p.Get(); v != Defaults() || v.History.RawRetention != 168*time.Hour || v.Logging.Format != "json" || !v.Metrics.Enabled {
		t.Fatalf("%+v", v)
	}
	if err := Validate(Defaults()); err != nil {
		t.Fatalf("the defaults must be valid: %v", err)
	}
}

func TestUpdateSavesAppliesAndSurvivesARestart(t *testing.T) {
	p, st, box := newPrefs(t)
	ctx := context.Background()
	var applied []Values
	p.OnApply(func(v Values) { applied = append(applied, v) })
	changed, err := p.Update(ctx, Patch{
		Notify:  &NotifyPrefs{QuietHours: "23:00-07:00", Timezone: "Asia/Jerusalem"},
		Metrics: &MetricsPatch{Enabled: true, Path: "/prom", PerClient: "all", Token: ptr("scrape-me")},
		Logging: &LoggingPrefs{Level: "debug", Format: "text"},
	})
	if err != nil || len(changed) != 3 {
		t.Fatal(changed, err)
	}
	if last := applied[len(applied)-1]; last.Notify.Timezone != "Asia/Jerusalem" || last.Metrics.Path != "/prom" || last.Logging.Level != "debug" || last.Logging.Format != "text" {
		t.Fatalf("not applied: %+v", last)
	}
	// The token is sealed in the database.
	var raw map[string]any
	st.GetSetting(ctx, Metrics, &raw)
	if tok, _ := raw["token"].(string); !strings.HasPrefix(tok, "v1:") {
		t.Fatalf("the token must be stored sealed: %v", raw)
	}
	// A restart reads the saved values back.
	p2 := New(st, box)
	if err := p2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	v := p2.Get()
	if v.Metrics.Token != "scrape-me" || v.Notify.QuietHours != "23:00-07:00" || v.Logging.Format != "text" || v.History != Defaults().History {
		t.Fatalf("%+v", v)
	}
	// A nil token keeps it; "" clears it.
	p2.Update(ctx, Patch{Metrics: &MetricsPatch{Enabled: false, Path: "/prom", PerClient: "none"}})
	if p2.Get().Metrics.Token != "scrape-me" {
		t.Fatal("a nil token must keep the saved one")
	}
	p2.Update(ctx, Patch{Metrics: &MetricsPatch{Enabled: false, Path: "/prom", PerClient: "none", Token: ptr("")}})
	if p2.Get().Metrics.Token != "" {
		t.Fatal("an empty token must clear it")
	}
}

func TestUpdateIsAllOrNothing(t *testing.T) {
	p, st, _ := newPrefs(t)
	ctx := context.Background()
	_, err := p.Update(ctx, Patch{
		Logging: &LoggingPrefs{Level: "debug", Format: "json"},
		Notify:  &NotifyPrefs{Timezone: "Mars/Base"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	if p.Get().Logging.Level != "info" {
		t.Fatal("a valid section in a failed request must not be applied")
	}
	if ok, _ := st.GetSetting(ctx, Logging, new(LoggingPrefs)); ok {
		t.Fatal("nothing may be saved")
	}
}

func TestValidation(t *testing.T) {
	ok := Defaults()
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Values){
		"raw too short":     func(v *Values) { v.History.RawRetention = time.Minute },
		"rollup < raw":      func(v *Values) { v.History.RollupRetention = time.Hour },
		"metrics path /":    func(v *Values) { v.Metrics.Path = "/" },
		"metrics under api": func(v *Values) { v.Metrics.Path = "/api/v1/x" },
		"metrics on health": func(v *Values) { v.Metrics.Path = "/health" },
		"metrics on a page": func(v *Values) { v.Metrics.Path = "/settings" },
		"per client":        func(v *Values) { v.Metrics.PerClient = "some" },
		"quiet hours":       func(v *Values) { v.Notify.QuietHours = "25:00-07:00" },
		"timezone":          func(v *Values) { v.Notify.Timezone = "" },
		"base url":          func(v *Values) { v.Access.BaseURL = "atlantis.lan" },
		"log level":         func(v *Values) { v.Logging.Level = "loud" },
		"log format":        func(v *Values) { v.Logging.Format = "xml" },
	} {
		v := ok
		mod(&v)
		if err := Validate(v); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// One bad section neither blocks saving another nor drops the rest.
func TestSectionsAreIndependent(t *testing.T) {
	p, st, box := newPrefs(t)
	ctx := context.Background()
	if _, err := p.Update(ctx, Patch{Logging: &LoggingPrefs{Level: "debug", Format: "json"}}); err != nil {
		t.Fatal(err)
	}
	// A value in another section that would not validate doesn't block a save.
	p.v.Access.BaseURL = "atlantis.lan"
	if _, err := p.Update(ctx, Patch{Debug: &DebugPrefs{Enabled: true}}); err != nil {
		t.Fatalf("saving debug must not fail on the public address: %v", err)
	}
	// A saved section that no longer validates falls back alone.
	st.SetSetting(ctx, History, HistoryPrefs{RawRetention: time.Minute, RollupRetention: time.Hour})
	p3 := New(st, box)
	if err := p3.Load(ctx); err == nil {
		t.Fatal("the bad section must be reported")
	}
	if v := p3.Get(); v.History != Defaults().History || v.Logging.Level != "debug" || !v.Debug.Enabled {
		t.Fatal("only the bad section may fall back")
	}
}

// A level saved before the format existed keeps the default format.
func TestOldLoggingRowKeepsTheDefaultFormat(t *testing.T) {
	p, st, _ := newPrefs(t)
	ctx := context.Background()
	st.SetSetting(ctx, Logging, map[string]string{"level": "warn"})
	if err := p.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if l := p.Get().Logging; l.Level != "warn" || l.Format != "json" {
		t.Fatalf("%+v", l)
	}
}

// An API call may change just the level (or just the format).
func TestLoggingFieldsKeepTheirValue(t *testing.T) {
	p, _, _ := newPrefs(t)
	ctx := context.Background()
	if _, err := p.Update(ctx, Patch{Logging: &LoggingPrefs{Format: "text"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Update(ctx, Patch{Logging: &LoggingPrefs{Level: "warn"}}); err != nil {
		t.Fatal(err)
	}
	if l := p.Get().Logging; l.Level != "warn" || l.Format != "text" {
		t.Fatalf("%+v", l)
	}
}

func TestTrustNewHostKeyIsOnUntilTurnedOff(t *testing.T) {
	p, st, box := newPrefs(t)
	ctx := context.Background()
	if !p.Get().Security.TrustNewHostKey {
		t.Fatal("on by default")
	}
	if _, err := p.Update(ctx, Patch{Security: &SecurityPrefs{TrustNewHostKey: false}}); err != nil {
		t.Fatal(err)
	}
	p2 := New(st, box)
	if err := p2.Load(ctx); err != nil || p2.Get().Security.TrustNewHostKey {
		t.Fatalf("turning it off survives a restart: %v %+v", err, p2.Get().Security)
	}
}
