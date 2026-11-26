package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/t0mer/atlantis/internal/events"
	"github.com/t0mer/atlantis/internal/prefs"
	"github.com/t0mer/atlantis/internal/site"
)

// Startup describes what is fixed when Atlantis starts (shown read-only in
// Settings).
type Startup struct {
	Listen    string `json:"listen"`
	DataPath  string `json:"data_path"`
	KeySource string `json:"key_source"` // "environment (...)" or the key file's path
}

// settings reports every setting and the startup facts. Secrets are never
// returned, only whether they are set.
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	n, _, err := s.store.GetNetwork(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	p := s.Site.Poll()
	v := s.Prefs.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"deco":    map[string]any{"transport": n.Transport, "host": n.Host, "port": n.Port, "email": n.Email},
		"poll":    map[string]any{"fast": p.Fast.String(), "slow": p.Slow.String(), "offline_after_misses": p.OfflineAfterMisses},
		"history": map[string]any{"raw_retention": v.History.RawRetention.String(), "rollup_retention": v.History.RollupRetention.String()},
		"metrics": map[string]any{"enabled": v.Metrics.Enabled, "path": v.Metrics.Path, "per_client": v.Metrics.PerClient,
			"token_set": v.Metrics.Token != ""},
		"notifications": map[string]any{"quiet_hours": v.Notify.QuietHours, "timezone": v.Notify.Timezone, "events": events.All},
		"access":        map[string]any{"base_url": v.Access.BaseURL},
		"logging":       map[string]any{"level": v.Logging.Level, "format": v.Logging.Format},
		"debug":         map[string]any{"enabled": v.Debug.Enabled},
		"security":      map[string]any{"trust_new_host_key": v.Security.TrustNewHostKey},
		"startup":       s.Startup,
		"version":       s.Version,
	})
}

type settingsBody struct {
	Poll *struct {
		Fast               string `json:"fast"`
		Slow               string `json:"slow"`
		OfflineAfterMisses int    `json:"offline_after_misses"`
	} `json:"poll"`
	History *struct {
		RawRetention    string `json:"raw_retention"`
		RollupRetention string `json:"rollup_retention"`
	} `json:"history"`
	Metrics *struct {
		Enabled   bool    `json:"enabled"`
		Path      string  `json:"path"`
		PerClient string  `json:"per_client"`
		Token     *string `json:"token"` // absent = keep, "" = clear
	} `json:"metrics"`
	Notifications *prefs.NotifyPrefs  `json:"notifications"`
	Access        *prefs.AccessPrefs  `json:"access"`
	Logging       *prefs.LoggingPrefs `json:"logging"`
	Debug         *prefs.DebugPrefs   `json:"debug"`
	Security      *struct {
		TrustNewHostKey *bool `json:"trust_new_host_key"` // absent = keep
	} `json:"security"`
}

// putSettings changes any of the sections. Everything is checked before
// anything is saved; the UI settings are saved in one transaction. Refresh
// times are saved separately first, so a storage failure on the rest can
// leave them applied (the UI never sends both together).
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var b settingsBody
	if !readJSON(w, r, &b) {
		return
	}
	bad := func(msg string) { writeError(w, http.StatusBadRequest, msg) }
	var poll *site.Poll
	if b.Poll != nil {
		fast, err1 := time.ParseDuration(b.Poll.Fast)
		slow, err2 := time.ParseDuration(b.Poll.Slow)
		if err1 != nil || err2 != nil {
			bad(`intervals are durations such as "30s" or "5m"`)
			return
		}
		poll = &site.Poll{Fast: fast, Slow: slow, OfflineAfterMisses: b.Poll.OfflineAfterMisses}
		if err := poll.Validate(); err != nil {
			bad(err.Error())
			return
		}
	}
	var pt prefs.Patch
	if b.History != nil {
		raw, err1 := time.ParseDuration(b.History.RawRetention)
		rollup, err2 := time.ParseDuration(b.History.RollupRetention)
		if err1 != nil || err2 != nil {
			bad(`retention is a duration such as "168h"`)
			return
		}
		pt.History = &prefs.HistoryPrefs{RawRetention: raw, RollupRetention: rollup}
	}
	if m := b.Metrics; m != nil {
		pt.Metrics = &prefs.MetricsPatch{Enabled: m.Enabled, Path: strings.TrimSpace(m.Path), PerClient: m.PerClient, Token: m.Token}
	}
	pt.Access, pt.Logging, pt.Debug = b.Access, b.Logging, b.Debug
	if b.Security != nil && b.Security.TrustNewHostKey != nil {
		pt.Security = &prefs.SecurityPrefs{TrustNewHostKey: *b.Security.TrustNewHostKey}
	}
	if n := b.Notifications; n != nil {
		pt.Notify = &prefs.NotifyPrefs{QuietHours: strings.TrimSpace(n.QuietHours), Timezone: strings.TrimSpace(n.Timezone)}
	}
	if poll == nil && pt == (prefs.Patch{}) {
		bad("nothing to change")
		return
	}
	user := userFrom(r.Context())
	hasPrefs := pt != (prefs.Patch{})
	// Check everything before saving anything, so a request changes all
	// of it or none (short of a storage failure).
	if hasPrefs {
		if err := s.Prefs.Check(pt); err != nil {
			s.audit(r.Context(), user, "settings.update", strings.Join(pt.Sections(), ","), err)
			bad(err.Error())
			return
		}
	}
	if poll != nil {
		err := s.Site.SetPoll(r.Context(), *poll)
		s.audit(r.Context(), user, "settings.poll", fmt.Sprintf("fast=%s slow=%s offline_after=%d", poll.Fast, poll.Slow, poll.OfflineAfterMisses), err)
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if hasPrefs {
		_, err := s.Prefs.Update(r.Context(), pt)
		s.audit(r.Context(), user, "settings.update", strings.Join(pt.Sections(), ","), err)
		if errors.Is(err, prefs.ErrInvalid) {
			bad(err.Error())
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	s.settings(w, r)
}
