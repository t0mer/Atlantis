// Package prefs holds the settings that are edited in the web UI (Settings)
// and saved in the database. Values from the YAML file or the environment
// are only defaults: a value saved in the UI always wins. Each change is
// validated, saved and then applied to the running components through
// callbacks, so nothing needs a restart.
package prefs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

// ErrInvalid marks a value the UI may not save.
var ErrInvalid = errors.New("invalid setting")

// Section names, also the settings keys in the database.
const (
	History = "history"
	Metrics = "metrics"
	Notify  = "notifications"
	OpenWA  = "openwa"
	Access  = "access"
	Logging = "logging"
	Debug   = "debug"
)

// Sections lists every section, in display order.
var Sections = []string{History, Metrics, Notify, OpenWA, Access, Logging, Debug}

// Values are the settings in effect. Secrets (Metrics.Token, OpenWA.APIKey)
// are plain here; they are sealed in the database and never sent to the UI.
type Values struct {
	History HistoryPrefs
	Metrics config.Metrics
	Notify  config.Notify
	OpenWA  config.OpenWA
	Access  AccessPrefs
	Logging LoggingPrefs
	Debug   config.Debug
}

// HistoryPrefs is how long history is kept.
type HistoryPrefs struct {
	RawRetention    time.Duration `json:"raw_retention"`
	RollupRetention time.Duration `json:"rollup_retention"`
}

// AccessPrefs is how Atlantis is reached.
type AccessPrefs struct {
	BaseURL string `json:"base_url"` // public URL; https:// marks the session cookie Secure
}

// LoggingPrefs is the log level (the format is set at startup).
type LoggingPrefs struct {
	Level string `json:"level"`
}

// Patch changes some sections. Nil sections stay as they are. For the
// secrets, a nil pointer keeps the saved value and "" clears it.
type Patch struct {
	History *HistoryPrefs
	Metrics *MetricsPatch
	Notify  *config.Notify
	OpenWA  *OpenWAPatch
	Access  *AccessPrefs
	Logging *LoggingPrefs
	Debug   *config.Debug
}

// MetricsPatch is config.Metrics with a keep-or-replace token.
type MetricsPatch struct {
	Enabled   bool
	Path      string
	PerClient string
	Token     *string
}

// OpenWAPatch is config.OpenWA with a keep-or-replace API key.
type OpenWAPatch struct {
	URL         string
	DefaultChat string
	APIKey      *string
}

// Prefs is the store of UI settings. It is safe for concurrent use.
type Prefs struct {
	st  *store.Store
	box *secret.Box

	mu     sync.RWMutex
	v      Values
	fromUI map[string]bool

	set     sync.Mutex // serialises Update: what is saved is what runs
	onApply []func(Values)
}

// New starts from the configuration's values.
func New(cfg *config.Config, st *store.Store, box *secret.Box) *Prefs {
	return &Prefs{st: st, box: box, fromUI: map[string]bool{}, v: Values{
		History: HistoryPrefs{RawRetention: cfg.Store.RawRetention, RollupRetention: cfg.Store.RollupRetention},
		Metrics: cfg.Metrics,
		Notify:  cfg.Notify,
		OpenWA:  cfg.OpenWA,
		Access:  AccessPrefs{BaseURL: cfg.Server.BaseURL},
		Logging: LoggingPrefs{Level: cfg.Log.Level},
		Debug:   cfg.Debug,
	}}
}

// OnApply registers fn to receive the values now and after every change.
func (p *Prefs) OnApply(fn func(Values)) {
	p.set.Lock()
	defer p.set.Unlock()
	p.onApply = append(p.onApply, fn)
	fn(p.Get())
}

// Get returns the values in effect.
func (p *Prefs) Get() Values {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.v
}

// FromUI reports whether a section was saved in the UI (otherwise it comes
// from the configuration).
func (p *Prefs) FromUI(section string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.fromUI[section]
}

// Load reads the sections saved in the UI. A saved section that no longer
// validates is ignored with an error, and the configuration's value stays.
func (p *Prefs) Load(ctx context.Context) error {
	p.set.Lock()
	defer p.set.Unlock()
	v := p.Get()
	from := map[string]bool{}
	var errs []error
	load := func(section string, into any) bool {
		ok, err := p.st.GetSetting(ctx, section, into)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", section, err))
		}
		return ok && err == nil
	}
	var h HistoryPrefs
	if load(History, &h) {
		v.History, from[History] = h, true
	}
	var m savedMetrics
	if load(Metrics, &m) {
		if tok, err := p.box.Open(m.Token); err != nil {
			errs = append(errs, fmt.Errorf("metrics token: %w", err))
		} else {
			v.Metrics = config.Metrics{Enabled: m.Enabled, Path: m.Path, PerClient: m.PerClient, Token: tok}
			from[Metrics] = true
		}
	}
	var n config.Notify
	if load(Notify, &n) {
		v.Notify, from[Notify] = n, true
	}
	var o savedOpenWA
	if load(OpenWA, &o) {
		if key, err := p.box.Open(o.APIKey); err != nil {
			errs = append(errs, fmt.Errorf("openwa key: %w", err))
		} else {
			v.OpenWA = config.OpenWA{URL: o.URL, DefaultChat: o.DefaultChat, APIKey: key}
			from[OpenWA] = true
		}
	}
	var a AccessPrefs
	if load(Access, &a) {
		v.Access, from[Access] = a, true
	}
	var l LoggingPrefs
	if load(Logging, &l) {
		v.Logging, from[Logging] = l, true
	}
	var d config.Debug
	if load(Debug, &d) {
		v.Debug, from[Debug] = d, true
	}
	// The configuration's own values were checked by config.Validate; only
	// a mix with saved sections needs checking here.
	if len(from) > 0 {
		if err := Validate(v); err != nil {
			return errors.Join(append(errs, fmt.Errorf("saved settings are not valid, using the configuration: %w", err))...)
		}
	}
	p.mu.Lock()
	p.v, p.fromUI = v, from
	p.mu.Unlock()
	for _, fn := range p.onApply {
		fn(v)
	}
	return errors.Join(errs...)
}

type savedMetrics struct {
	Enabled   bool   `json:"enabled"`
	Path      string `json:"path"`
	PerClient string `json:"per_client"`
	Token     string `json:"token"` // sealed
}

type savedOpenWA struct {
	URL         string `json:"url"`
	DefaultChat string `json:"default_chat"`
	APIKey      string `json:"api_key"` // sealed
}

// Update validates every section in the patch, then saves and applies them
// all, so a request changes everything or nothing. It returns the names of
// the changed sections.
func (p *Prefs) Update(ctx context.Context, pt Patch) ([]string, error) {
	p.set.Lock()
	defer p.set.Unlock()
	v := p.Get()
	var changed []string
	if pt.History != nil {
		v.History = *pt.History
		changed = append(changed, History)
	}
	if pt.Metrics != nil {
		tok := v.Metrics.Token
		if pt.Metrics.Token != nil {
			tok = *pt.Metrics.Token
		}
		v.Metrics = config.Metrics{Enabled: pt.Metrics.Enabled, Path: pt.Metrics.Path, PerClient: pt.Metrics.PerClient, Token: tok}
		changed = append(changed, Metrics)
	}
	if pt.Notify != nil {
		v.Notify = *pt.Notify
		changed = append(changed, Notify)
	}
	if pt.OpenWA != nil {
		key := v.OpenWA.APIKey
		if pt.OpenWA.APIKey != nil {
			key = *pt.OpenWA.APIKey
		}
		v.OpenWA = config.OpenWA{URL: strings.TrimSpace(pt.OpenWA.URL), DefaultChat: strings.TrimSpace(pt.OpenWA.DefaultChat), APIKey: strings.TrimSpace(key)}
		changed = append(changed, OpenWA)
	}
	if pt.Access != nil {
		v.Access = AccessPrefs{BaseURL: strings.TrimRight(strings.TrimSpace(pt.Access.BaseURL), "/")}
		changed = append(changed, Access)
	}
	if pt.Logging != nil {
		v.Logging = *pt.Logging
		changed = append(changed, Logging)
	}
	if pt.Debug != nil {
		v.Debug = *pt.Debug
		changed = append(changed, Debug)
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("%w: nothing to change", ErrInvalid)
	}
	if err := Validate(v); err != nil {
		return nil, err
	}
	for _, s := range changed {
		if err := p.save(ctx, s, v); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	p.v = v
	for _, s := range changed {
		p.fromUI[s] = true
	}
	p.mu.Unlock()
	for _, fn := range p.onApply {
		fn(v)
	}
	return changed, nil
}

func (p *Prefs) save(ctx context.Context, section string, v Values) error {
	var val any
	switch section {
	case History:
		val = v.History
	case Metrics:
		tok, err := p.box.Seal(v.Metrics.Token)
		if err != nil {
			return err
		}
		val = savedMetrics{Enabled: v.Metrics.Enabled, Path: v.Metrics.Path, PerClient: v.Metrics.PerClient, Token: tok}
	case Notify:
		val = v.Notify
	case OpenWA:
		key, err := p.box.Seal(v.OpenWA.APIKey)
		if err != nil {
			return err
		}
		val = savedOpenWA{URL: v.OpenWA.URL, DefaultChat: v.OpenWA.DefaultChat, APIKey: key}
	case Access:
		val = v.Access
	case Logging:
		val = v.Logging
	case Debug:
		val = v.Debug
	}
	return p.st.SetSetting(ctx, section, val)
}

// Validate checks every section.
func Validate(v Values) error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...))
	}

	h := v.History
	if h.RawRetention < time.Hour || h.RawRetention > 365*24*time.Hour {
		bad("raw samples are kept from 1 hour to 1 year")
	}
	if h.RollupRetention < h.RawRetention || h.RollupRetention > 10*365*24*time.Hour {
		bad("hourly history is kept at least as long as raw samples, and at most 10 years")
	}

	m := v.Metrics
	if !strings.HasPrefix(m.Path, "/") || m.Path == "/" || strings.HasPrefix(m.Path, "/api") || strings.ContainsAny(m.Path, " ?#") {
		bad("the metrics path must start with / and not be / or under /api")
	}
	switch m.PerClient {
	case "all", "watched", "none":
	default:
		bad("per-device metrics must be all, watched or none")
	}
	if len(m.Token) > 256 {
		bad("the metrics token is too long")
	}

	n := v.Notify
	if n.QuietHours != "" {
		if _, _, err := config.ParseQuietHours(n.QuietHours); err != nil {
			bad("quiet hours: %v", err)
		}
	}
	if n.Timezone == "" {
		bad("the time zone is required")
	} else if _, err := time.LoadLocation(n.Timezone); err != nil {
		bad("unknown time zone %q", n.Timezone)
	}

	if err := checkURL(v.OpenWA.URL); err != nil {
		bad("OpenWA URL: %v", err)
	}
	if len(v.OpenWA.APIKey) > 512 || len(v.OpenWA.DefaultChat) > 128 {
		bad("OpenWA key or chat is too long")
	}

	if err := checkURL(v.Access.BaseURL); err != nil {
		bad("public URL: %v", err)
	}

	switch v.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		bad("the log level must be debug, info, warn or error")
	}
	return errors.Join(errs...)
}

// checkURL accepts "" or an absolute http(s) URL.
func checkURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http:// or https:// address")
	}
	return nil
}
