// Package prefs holds the settings that are edited in the web UI (Settings)
// and saved in the database; until a section is saved, its built-in
// default applies. Each change is validated, saved and then applied to the
// running components through callbacks, so nothing needs a restart.
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
	Access  = "access"
	Logging = "logging"
	Debug   = "debug"
)

// Sections lists every section, in display order.
var Sections = []string{History, Metrics, Notify, Access, Logging, Debug}

// Values are the settings in effect. The secret (Metrics.Token) is plain
// here; it is sealed in the database and never sent to the UI.
type Values struct {
	History HistoryPrefs
	Metrics MetricsPrefs
	Notify  NotifyPrefs
	Access  AccessPrefs
	Logging LoggingPrefs
	Debug   DebugPrefs
}

// Defaults are the values used until a section is saved.
func Defaults() Values {
	return Values{
		History: HistoryPrefs{RawRetention: 7 * 24 * time.Hour, RollupRetention: 90 * 24 * time.Hour},
		Metrics: MetricsPrefs{Enabled: true, Path: "/metrics", PerClient: "watched"},
		Notify:  NotifyPrefs{Timezone: "UTC"},
		Logging: LoggingPrefs{Level: "info", Format: "json"},
	}
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

// MetricsPrefs is the Prometheus endpoint.
type MetricsPrefs struct {
	Enabled   bool
	Path      string
	PerClient string // all, watched, none
	// Token is the bearer token Prometheus sends to scrape metrics. Without
	// one, metrics need a logged-in session (they are never public).
	Token string
}

// NotifyPrefs is when notifications are held back.
type NotifyPrefs struct {
	QuietHours string `json:"quiet_hours"` // "23:00-07:00", empty = none
	Timezone   string `json:"timezone"`
}

// LoggingPrefs is the log level and format.
type LoggingPrefs struct {
	Level  string `json:"level"`  // debug, info, warn, error
	Format string `json:"format"` // json, text
}

// DebugPrefs turns on the raw Deco endpoint.
type DebugPrefs struct {
	Enabled bool `json:"enabled"`
}

// Patch changes some sections. Nil sections stay as they are. For the
// secrets, a nil pointer keeps the saved value and "" clears it.
type Patch struct {
	History *HistoryPrefs
	Metrics *MetricsPatch
	Notify  *NotifyPrefs
	Access  *AccessPrefs
	Logging *LoggingPrefs
	Debug   *DebugPrefs
}

// MetricsPatch is MetricsPrefs with a keep-or-replace token.
type MetricsPatch struct {
	Enabled   bool
	Path      string
	PerClient string
	Token     *string
}

// Prefs is the store of UI settings. It is safe for concurrent use.
type Prefs struct {
	st  *store.Store
	box *secret.Box

	mu sync.RWMutex
	v  Values

	set     sync.Mutex // serialises Update: what is saved is what runs
	onApply []func(Values)
}

// New starts from the defaults; Load reads what was saved.
func New(st *store.Store, box *secret.Box) *Prefs {
	return &Prefs{st: st, box: box, v: Defaults()}
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

// Load reads the saved sections. A saved section that no longer validates
// is ignored with an error, and its default applies.
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
			v.Metrics = MetricsPrefs{Enabled: m.Enabled, Path: m.Path, PerClient: m.PerClient, Token: tok}
			from[Metrics] = true
		}
	}
	var n NotifyPrefs
	if load(Notify, &n) {
		v.Notify, from[Notify] = n, true
	}
	var a AccessPrefs
	if load(Access, &a) {
		v.Access, from[Access] = a, true
	}
	l := Defaults().Logging // a level saved before the format existed keeps the default format
	if load(Logging, &l) {
		v.Logging, from[Logging] = l, true
	}
	var d DebugPrefs
	if load(Debug, &d) {
		v.Debug, from[Debug] = d, true
	}
	// A saved section that no longer validates falls back to its default on
	// its own; the other sections are kept.
	def := Defaults()
	for sec := range from {
		if err := validateSection(sec, v); err != nil {
			errs = append(errs, fmt.Errorf("saved %s settings are not valid, using the default: %w", sec, err))
			setSection(&v, def, sec)
		}
	}
	p.mu.Lock()
	p.v = v
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

// apply returns the values with the patch applied and the changed
// sections, validating only those sections.
func (p *Prefs) apply(pt Patch) (Values, []string, error) {
	v := p.Get()
	var changed []string
	if pt.History != nil {
		v.History = *pt.History
		changed = append(changed, History)
	}
	if pt.Metrics != nil {
		tok := v.Metrics.Token
		if pt.Metrics.Token != nil {
			tok = strings.TrimSpace(*pt.Metrics.Token)
		}
		v.Metrics = MetricsPrefs{Enabled: pt.Metrics.Enabled, Path: strings.TrimSpace(pt.Metrics.Path), PerClient: pt.Metrics.PerClient, Token: tok}
		changed = append(changed, Metrics)
	}
	if pt.Notify != nil {
		v.Notify = *pt.Notify
		changed = append(changed, Notify)
	}
	if pt.Access != nil {
		v.Access = AccessPrefs{BaseURL: normalizeURL(pt.Access.BaseURL)}
		changed = append(changed, Access)
	}
	if pt.Logging != nil {
		l := *pt.Logging // an empty field keeps its value
		if l.Level == "" {
			l.Level = v.Logging.Level
		}
		if l.Format == "" {
			l.Format = v.Logging.Format
		}
		v.Logging = l
		changed = append(changed, Logging)
	}
	if pt.Debug != nil {
		v.Debug = *pt.Debug
		changed = append(changed, Debug)
	}
	if len(changed) == 0 {
		return v, nil, fmt.Errorf("%w: nothing to change", ErrInvalid)
	}
	var errs []error
	for _, sec := range changed {
		errs = append(errs, validateSection(sec, v))
	}
	return v, changed, errors.Join(errs...)
}

// Check validates a patch without saving it.
func (p *Prefs) Check(pt Patch) error {
	_, _, err := p.apply(pt)
	return err
}

// Sections returns the section names a patch touches.
func (pt Patch) Sections() []string {
	var out []string
	for _, s := range []struct {
		name string
		set  bool
	}{{History, pt.History != nil}, {Metrics, pt.Metrics != nil}, {Notify, pt.Notify != nil},
		{Access, pt.Access != nil}, {Logging, pt.Logging != nil}, {Debug, pt.Debug != nil}} {
		if s.set {
			out = append(out, s.name)
		}
	}
	return out
}

// Update validates the sections in the patch, saves them in one
// transaction and applies them, so a request changes all of them or none.
// It returns the names of the changed sections.
func (p *Prefs) Update(ctx context.Context, pt Patch) ([]string, error) {
	p.set.Lock()
	defer p.set.Unlock()
	v, changed, err := p.apply(pt)
	if err != nil {
		return nil, err
	}
	vals := map[string]any{}
	for _, sec := range changed {
		val, err := p.stored(sec, v)
		if err != nil {
			return nil, err
		}
		vals[sec] = val
	}
	if err := p.st.SetSettings(ctx, vals); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.v = v
	p.mu.Unlock()
	for _, fn := range p.onApply {
		fn(v)
	}
	return changed, nil
}

// stored is the value saved for a section, with secrets sealed.
func (p *Prefs) stored(section string, v Values) (any, error) {
	var val any
	switch section {
	case History:
		val = v.History
	case Metrics:
		tok, err := p.box.Seal(v.Metrics.Token)
		if err != nil {
			return nil, err
		}
		val = savedMetrics{Enabled: v.Metrics.Enabled, Path: v.Metrics.Path, PerClient: v.Metrics.PerClient, Token: tok}
	case Notify:
		val = v.Notify
	case Access:
		val = v.Access
	case Logging:
		val = v.Logging
	case Debug:
		val = v.Debug
	}
	return val, nil
}

// setSection copies one section from src into v.
func setSection(v *Values, src Values, section string) {
	switch section {
	case History:
		v.History = src.History
	case Metrics:
		v.Metrics = src.Metrics
	case Notify:
		v.Notify = src.Notify
	case Access:
		v.Access = src.Access
	case Logging:
		v.Logging = src.Logging
	case Debug:
		v.Debug = src.Debug
	}
}

// normalizeURL trims a URL and lowercases its scheme.
func normalizeURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if scheme, rest, ok := strings.Cut(s, "://"); ok {
		s = strings.ToLower(scheme) + "://" + rest
	}
	return s
}

// reservedPaths are Atlantis's own routes; the metrics path may not take one.
var reservedPaths = []string{"/api", "/assets", "/health", "/clients", "/nodes", "/wifi", "/events", "/settings"}

// Validate checks every section.
func Validate(v Values) error {
	var errs []error
	for _, sec := range Sections {
		errs = append(errs, validateSection(sec, v))
	}
	return errors.Join(errs...)
}

func validateSection(section string, v Values) error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...))
	}
	switch section {
	case History:
		h := v.History
		if h.RawRetention < time.Hour || h.RawRetention > 365*24*time.Hour {
			bad("raw samples are kept from 1 hour to 1 year")
		}
		if h.RollupRetention < h.RawRetention || h.RollupRetention > 10*365*24*time.Hour {
			bad("hourly history is kept at least as long as raw samples, and at most 10 years")
		}
	case Metrics:
		m := v.Metrics
		reserved := false
		for _, r := range reservedPaths {
			if m.Path == r || strings.HasPrefix(m.Path, r+"/") {
				reserved = true
			}
		}
		if !strings.HasPrefix(m.Path, "/") || m.Path == "/" || reserved || strings.ContainsAny(m.Path, " ?#") {
			bad("the metrics path must start with / and not be one of Atlantis's own pages (%s)", strings.Join(reservedPaths, ", "))
		}
		switch m.PerClient {
		case "all", "watched", "none":
		default:
			bad("per-device metrics must be all, watched or none")
		}
		if len(m.Token) > 256 {
			bad("the metrics token is too long")
		}
	case Notify:
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
	case Access:
		if err := checkURL(v.Access.BaseURL); err != nil {
			bad("public address: %v", err)
		}
	case Logging:
		switch v.Logging.Level {
		case "debug", "info", "warn", "error":
		default:
			bad("the log level must be debug, info, warn or error")
		}
		switch v.Logging.Format {
		case "json", "text":
		default:
			bad("the log format must be json or text")
		}
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
