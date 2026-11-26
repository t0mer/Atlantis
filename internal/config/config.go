// Package config reads the few settings Atlantis needs before its database
// is open: where to listen, where the database is, and (optionally) the
// encryption key. Everything else is set in the web UI and kept in the
// database. A flag wins over its ATLANTIS_* environment variable; both are
// optional.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/t0mer/atlantis/internal/secret"
)

// Config is what is fixed when Atlantis starts.
type Config struct {
	Listen    string // --server-listen, ATLANTIS_SERVER_LISTEN
	StorePath string // --store-path, ATLANTIS_STORE_PATH
	// SecretKey is the base64 32-byte key that encrypts stored secrets
	// (ATLANTIS_SECRET_KEY). Without it, a key file is kept next to the
	// database and created on first start.
	SecretKey string
}

// Defaults.
const (
	DefaultListen = ":8080"
	// DefaultStorePath is relative to the working directory: /data in the
	// container image, the current directory otherwise.
	DefaultStorePath = "atlantis.db"
)

// MinPoll is the smallest allowed poll interval.
const MinPoll = 10 * time.Second

// Load reads the environment and then flags (flags may be nil).
func Load(flags *pflag.FlagSet) (*Config, error) {
	c := &Config{
		Listen:    os.Getenv("ATLANTIS_SERVER_LISTEN"),
		StorePath: os.Getenv("ATLANTIS_STORE_PATH"),
		SecretKey: os.Getenv("ATLANTIS_SECRET_KEY"),
	}
	if flags != nil {
		for name, into := range map[string]*string{"server-listen": &c.Listen, "store-path": &c.StorePath} {
			if f := flags.Lookup(name); f != nil && f.Changed {
				*into = f.Value.String()
			}
		}
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.StorePath == "" {
		c.StorePath = DefaultStorePath
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate reports every invalid setting at once.
func (c *Config) Validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen address %q: %v", c.Listen, err))
	}
	if c.StorePath == "" {
		errs = append(errs, errors.New("the database path is empty"))
	}
	if c.SecretKey != "" {
		if _, err := secret.New(c.SecretKey); err != nil {
			errs = append(errs, fmt.Errorf("ATLANTIS_SECRET_KEY: %v (generate one with `atlantis keygen`)", err))
		}
	}
	return errors.Join(errs...)
}

// obsolete are environment variables earlier versions read. Their settings
// are now made in the UI.
var obsolete = []string{
	"ATLANTIS_CONFIG", "ATLANTIS_SERVER_BASE_URL", "ATLANTIS_SERVER_TRUSTED_PROXIES",
	"ATLANTIS_DECO_TRANSPORT", "ATLANTIS_DECO_HOST", "ATLANTIS_DECO_PORT", "ATLANTIS_DECO_EMAIL",
	"ATLANTIS_DECO_PASSWORD", "ATLANTIS_DECO_HOST_KEY", "ATLANTIS_DECO_TLS_PIN",
	"ATLANTIS_POLL_FAST", "ATLANTIS_POLL_SLOW", "ATLANTIS_POLL_OFFLINE_AFTER_MISSES",
	"ATLANTIS_STORE_RAW_RETENTION", "ATLANTIS_STORE_ROLLUP_RETENTION",
	"ATLANTIS_METRICS_ENABLED", "ATLANTIS_METRICS_PATH", "ATLANTIS_METRICS_PER_CLIENT", "ATLANTIS_METRICS_TOKEN",
	"ATLANTIS_NOTIFY_QUIET_HOURS", "ATLANTIS_NOTIFY_TIMEZONE",
	"ATLANTIS_OPENWA_URL", "ATLANTIS_OPENWA_API_KEY", "ATLANTIS_OPENWA_DEFAULT_CHAT",
	"ATLANTIS_DEBUG_ENABLED", "ATLANTIS_LOG_LEVEL", "ATLANTIS_LOG_FORMAT",
}

// Obsolete lists the old environment variables that are still set, so
// the server can say they are no longer read.
func Obsolete() []string {
	var out []string
	for _, k := range obsolete {
		if _, ok := os.LookupEnv(k); ok {
			out = append(out, k)
		}
	}
	return out
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
