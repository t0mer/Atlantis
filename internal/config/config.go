// Package config loads Atlantis settings. Precedence: command-line flags,
// then ATLANTIS_* environment variables, then the YAML file, then defaults.
// One instance manages one Deco site over the LAN.
package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config is the full configuration.
type Config struct {
	Server  Server  `mapstructure:"server"`
	Deco    Deco    `mapstructure:"deco"`
	Poll    Poll    `mapstructure:"poll"`
	Store   Store   `mapstructure:"store"`
	Metrics Metrics `mapstructure:"metrics"`
	Notify  Notify  `mapstructure:"notify"`
	OpenWA  OpenWA  `mapstructure:"openwa"`
	Debug   Debug   `mapstructure:"debug"`
	Log     Log     `mapstructure:"log"`
	// SecretKey is the base64 32-byte key that encrypts stored secrets
	// (ATLANTIS_SECRET_KEY). Generate one with `atlantis keygen`.
	SecretKey string `mapstructure:"secret_key"`
}

type Server struct {
	Listen         string   `mapstructure:"listen"`
	BaseURL        string   `mapstructure:"base_url"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

// Deco is how to reach the site's main Deco.
type Deco struct {
	// Transport is "ssh" (the Deco app's LAN transport, default) or "luci"
	// (the local web API, where the firmware has it).
	Transport string `mapstructure:"transport"`
	Host      string `mapstructure:"host"`
	Port      int    `mapstructure:"port"` // SSH port; 0 = discover with TDP
	// Email and Password are the TP-Link ID (ssh) or the local management
	// password (luci). A password given here is moved, encrypted, into the
	// database on first start.
	Email    string `mapstructure:"email"`
	Password string `mapstructure:"password"`
	HostKey  string `mapstructure:"host_key"` // SSH host key fingerprint to pin
	TLSPin   string `mapstructure:"tls_pin"`  // luci over https
}

type Poll struct {
	Fast               time.Duration `mapstructure:"fast"`
	Slow               time.Duration `mapstructure:"slow"`
	OfflineAfterMisses int           `mapstructure:"offline_after_misses"`
}

type Store struct {
	Path            string        `mapstructure:"path"`
	RawRetention    time.Duration `mapstructure:"raw_retention"`
	RollupRetention time.Duration `mapstructure:"rollup_retention"`
}

type Metrics struct {
	Enabled   bool   `mapstructure:"enabled"`
	Path      string `mapstructure:"path"`
	PerClient string `mapstructure:"per_client"` // all, watched, none
	// Token is the bearer token Prometheus sends to scrape metrics. Without
	// one, metrics need a logged-in session (they are never public).
	Token string `mapstructure:"token"`
}

type Notify struct {
	QuietHours string `mapstructure:"quiet_hours"` // "23:00-07:00", empty = none
	Timezone   string `mapstructure:"timezone"`
}

type OpenWA struct {
	URL         string `mapstructure:"url"`
	APIKey      string `mapstructure:"api_key"`
	DefaultChat string `mapstructure:"default_chat"`
}

type Debug struct {
	Enabled bool `mapstructure:"enabled"`
}

type Log struct {
	Level  string `mapstructure:"level"`  // debug, info, warn, error
	Format string `mapstructure:"format"` // json, text
}

// MinPoll is the smallest allowed poll interval.
const MinPoll = 10 * time.Second

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.listen", ":8080")
	v.SetDefault("deco.transport", "ssh")
	v.SetDefault("poll.fast", 30*time.Second)
	v.SetDefault("poll.slow", 5*time.Minute)
	v.SetDefault("poll.offline_after_misses", 2)
	v.SetDefault("store.path", "/data/atlantis.db")
	v.SetDefault("store.raw_retention", 168*time.Hour)
	v.SetDefault("store.rollup_retention", 2160*time.Hour)
	v.SetDefault("metrics.enabled", true)
	v.SetDefault("metrics.path", "/metrics")
	v.SetDefault("metrics.per_client", "watched")
	v.SetDefault("notify.timezone", "UTC")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
}

// keys lists every setting, so environment variables bind even when the
// key is absent from the YAML file.
var keys = []string{
	"server.listen", "server.base_url", "server.trusted_proxies",
	"deco.transport", "deco.host", "deco.port", "deco.email", "deco.password", "deco.host_key", "deco.tls_pin",
	"poll.fast", "poll.slow", "poll.offline_after_misses",
	"store.path", "store.raw_retention", "store.rollup_retention",
	"metrics.enabled", "metrics.path", "metrics.per_client", "metrics.token",
	"notify.quiet_hours", "notify.timezone",
	"openwa.url", "openwa.api_key", "openwa.default_chat",
	"debug.enabled", "log.level", "log.format", "secret_key",
}

// Load reads the YAML file at path (optional; "" skips it), the
// environment and flags. Flags are bound by the setting name with dots
// replaced by dashes (e.g. --deco-host, --server-listen).
func Load(path string, flags *pflag.FlagSet) (*Config, error) {
	v := viper.New()
	setDefaults(v)
	v.SetEnvPrefix("ATLANTIS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	for _, k := range keys {
		if err := v.BindEnv(k); err != nil {
			return nil, err
		}
		if flags != nil {
			if f := flags.Lookup(strings.ReplaceAll(k, ".", "-")); f != nil {
				if err := v.BindPFlag(k, f); err != nil {
					return nil, err
				}
			}
		}
	}
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate reports every invalid setting at once.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		bad("server.listen %q: %v", c.Server.Listen, err)
	}
	switch c.Deco.Transport {
	case "ssh":
		if c.Deco.Email == "" {
			bad("deco.email (the TP-Link ID) is required for the ssh transport")
		}
	case "luci":
	default:
		bad("deco.transport must be ssh or luci, not %q", c.Deco.Transport)
	}
	if c.Deco.Host == "" {
		bad("deco.host (the main Deco's IP address) is required")
	}
	if c.Deco.Port < 0 || c.Deco.Port > 65535 {
		bad("deco.port %d is out of range", c.Deco.Port)
	}
	if c.Poll.Fast < MinPoll || c.Poll.Slow < MinPoll {
		bad("poll intervals must be at least %s (fast %s, slow %s)", MinPoll, c.Poll.Fast, c.Poll.Slow)
	}
	if c.Poll.OfflineAfterMisses < 1 {
		bad("poll.offline_after_misses must be at least 1")
	}
	if c.Store.Path == "" {
		bad("store.path is required")
	}
	if c.Store.RawRetention <= 0 || c.Store.RollupRetention < c.Store.RawRetention {
		bad("store retention: raw must be positive and rollup at least as long as raw")
	}
	switch c.Metrics.PerClient {
	case "all", "watched", "none":
	default:
		bad("metrics.per_client must be all, watched or none, not %q", c.Metrics.PerClient)
	}
	if !strings.HasPrefix(c.Metrics.Path, "/") {
		bad("metrics.path must start with /")
	}
	if _, err := time.LoadLocation(c.Notify.Timezone); err != nil {
		bad("notify.timezone %q: %v", c.Notify.Timezone, err)
	}
	if c.Notify.QuietHours != "" {
		if _, _, err := ParseQuietHours(c.Notify.QuietHours); err != nil {
			bad("notify.quiet_hours: %v", err)
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		bad("log.level must be debug, info, warn or error")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		bad("log.format must be json or text")
	}
	if c.SecretKey == "" {
		bad("ATLANTIS_SECRET_KEY is required (generate one with `atlantis keygen`)")
	}
	return errors.Join(errs...)
}

// ParseQuietHours parses "HH:MM-HH:MM" into minutes after midnight. The
// range may wrap past midnight.
func ParseQuietHours(s string) (start, end int, err error) {
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("%q: want HH:MM-HH:MM", s)
	}
	if start, err = clock(from); err == nil {
		end, err = clock(to)
	}
	return start, end, err
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q: want HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}
