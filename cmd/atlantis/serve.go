package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/t0mer/atlantis/internal/api"
	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/prefs"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/site"
	"github.com/t0mer/atlantis/internal/store"
	"github.com/t0mer/atlantis/internal/web"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Atlantis server: poll the Deco, keep history, serve the API and UI",
		Long: `Runs Atlantis for one Deco site on the LAN. Everything is set up in the web
UI and kept in the database; only where to listen and where the database is
can be given here (or in ATLANTIS_SERVER_LISTEN and ATLANTIS_STORE_PATH). The
key that encrypts stored passwords is created as secret.key next to the
database; ATLANTIS_SECRET_KEY can supply it instead.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Flags())
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log := newLogger(os.Stderr)
			warnRemovedFlags(cmd, log)
			return serve(ctx, cfg, log)
		},
	}
	f := cmd.Flags()
	f.String("server-listen", config.DefaultListen, "listen address (or ATLANTIS_SERVER_LISTEN)")
	f.String("store-path", config.DefaultStorePath, "SQLite database path (or ATLANTIS_STORE_PATH)")
	addRemovedFlags(cmd, "config", "deco-host", "deco-transport", "deco-email", "log-level")
	return cmd
}

// addRemovedFlags keeps flags earlier versions had, hidden and ignored, so
// an existing command line still starts.
func addRemovedFlags(cmd *cobra.Command, names ...string) {
	for _, n := range names {
		cmd.Flags().String(n, "", "no longer used")
		_ = cmd.Flags().MarkHidden(n)
	}
}

// warnRemovedFlags names the ignored flags that were given.
func warnRemovedFlags(cmd *cobra.Command, log *slog.Logger) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden && f.Changed {
			log.Warn("--" + f.Name + " is no longer used; make this setting in the web UI (Settings)")
		}
	})
}

// secretAttr reports attribute keys whose values must never be logged.
func secretAttr(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"password", "passwd", "secret", "token", "stok", "sysauth", "cookie", "api_key", "apikey"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// The process's log level and format; Settings changes them at runtime.
var (
	logLevel = new(slog.LevelVar)
	logText  = new(atomic.Bool)
)

// setLogging applies a level (debug, info, warn, error) and a format
// (json, text).
func setLogging(level, format string) {
	var lvl slog.Level
	if lvl.UnmarshalText([]byte(level)) == nil {
		logLevel.Set(lvl)
	}
	logText.Store(format == "text")
}

// newLogger logs JSON until Settings choose otherwise, never logging a
// secret attribute's value.
func newLogger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: logLevel, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if secretAttr(a.Key) {
			return slog.String(a.Key, "[redacted]")
		}
		return a
	}}
	return slog.New(formatHandler{json: slog.NewJSONHandler(w, opts), text: slog.NewTextHandler(w, opts), useText: logText})
}

// formatHandler writes each record as JSON or text, whichever is current.
// Both handlers carry the same attributes and groups.
type formatHandler struct {
	json, text slog.Handler
	useText    *atomic.Bool
}

func (h formatHandler) pick() slog.Handler {
	if h.useText.Load() {
		return h.text
	}
	return h.json
}

func (h formatHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.pick().Enabled(ctx, l)
}

func (h formatHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.pick().Handle(ctx, r)
}

func (h formatHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return formatHandler{json: h.json.WithAttrs(a), text: h.text.WithAttrs(a), useText: h.useText}
}

func (h formatHandler) WithGroup(name string) slog.Handler {
	return formatHandler{json: h.json.WithGroup(name), text: h.text.WithGroup(name), useText: h.useText}
}

func serve(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	// Shoutrrr uses http.DefaultClient, which has no timeout; bound it so an
	// abandoned notification send cannot hang for ever.
	http.DefaultClient.Timeout = 30 * time.Second

	for _, k := range config.Obsolete() {
		log.Warn(k + " is no longer read; make this setting in the web UI (Settings)")
	}
	cfg.StorePath = legacyStorePath(cfg.StorePath, log)
	st, err := store.Open(ctx, cfg.StorePath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.StorePath, err)
	}
	defer st.Close()
	box, keySource, err := openBox(ctx, cfg, st, log)
	if err != nil {
		return err
	}

	// Everything else is set in the UI (Settings).
	pf := prefs.New(st, box)
	if err := pf.Load(ctx); err != nil {
		log.Error("reading saved settings", "err", err)
	}
	// Metrics are always collected (cheap); the API serves them only while
	// enabled in Settings, at the path set there.
	m := metrics.New(pf.Get().Metrics.PerClient)
	hub := poller.NewHub()

	var sites *site.Site
	siteName := func() string {
		run := sites.Current()
		if run == nil {
			return "Atlantis"
		}
		for _, n := range run.Poller.Snapshot().Nodes {
			if n.Master && n.Nickname != "" {
				return n.Nickname
			}
		}
		return run.Network.Host
	}
	disp := notify.New(st, box, siteName, log)
	if err := moveOpenWADefaults(ctx, st, box, disp, log); err != nil {
		log.Error("moving the old OpenWA defaults into the WhatsApp channels", "err", err)
	}
	pf.OnApply(func(v prefs.Values) {
		disp.SetPrefs(v.Notify.QuietHours, v.Notify.Timezone)
		m.SetPerClient(v.Metrics.PerClient)
		setLogging(v.Logging.Level, v.Logging.Format)
	})
	retention := func() (time.Duration, time.Duration) {
		h := pf.Get().History
		return h.RawRetention, h.RollupRetention
	}
	sites = site.New(ctx, site.Deps{Store: st, Box: box, Hub: hub, Notify: disp, Metrics: m, Log: log, Retention: retention})
	if err := sites.LoadPoll(ctx); err != nil {
		return err
	}
	if err := sites.Start(ctx); err != nil {
		// Keep serving so the connection can be fixed from the UI.
		log.Error("starting the Deco connection", "err", err)
	}
	defer sites.Close(context.Background())

	dataPath := cfg.StorePath
	if abs, err := filepath.Abs(dataPath); err == nil {
		dataPath = abs
	}
	startup := api.Startup{Listen: cfg.Listen, DataPath: dataPath, KeySource: keySource}
	srv := api.New(api.Deps{Store: st, Site: sites, Prefs: pf, Startup: startup, Hub: hub, Notify: disp,
		Metrics: m.Handler(), Web: web.Handler(), Version: version, Log: log})
	// Request contexts end with ctx, so live streams stop on shutdown.
	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx }}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); disp.Run(ctx) }()
	errc := make(chan error, 1)
	go func() {
		host := "none (add it in the UI)"
		if run := sites.Current(); run != nil {
			host = run.Network.Host
		}
		log.Info("listening", "addr", cfg.Listen, "deco", host, "version", version)
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	wg.Wait()
	return nil
}

// openwaMoved records that moveOpenWADefaults has run.
const openwaMoved = "migrated.openwa_defaults"

// moveOpenWADefaults copies the OpenWA defaults earlier versions had (saved
// in Settings, or ATLANTIS_OPENWA_*) into the WhatsApp channels that left
// those fields empty, then deletes the saved ones. It runs once: later
// changes to a channel are never overwritten. Channels now carry their own
// address, chat and key.
func moveOpenWADefaults(ctx context.Context, st *store.Store, box *secret.Box, disp *notify.Dispatcher, log *slog.Logger) error {
	var done bool
	if ok, err := st.GetSetting(ctx, openwaMoved, &done); err != nil || (ok && done) {
		return err
	}
	var saved struct {
		URL         string `json:"url"`
		DefaultChat string `json:"default_chat"`
		APIKey      string `json:"api_key"` // sealed
	}
	have, err := st.GetSetting(ctx, "openwa", &saved)
	if err != nil {
		return err
	}
	def := notify.Config{URL: saved.URL, Chat: saved.DefaultChat}
	if have {
		if def.APIKey, err = box.Open(saved.APIKey); err != nil {
			return err
		}
	}
	for into, env := range map[*string]string{&def.URL: "ATLANTIS_OPENWA_URL", &def.APIKey: "ATLANTIS_OPENWA_API_KEY", &def.Chat: "ATLANTIS_OPENWA_DEFAULT_CHAT"} {
		if *into == "" {
			*into = strings.TrimSpace(os.Getenv(env))
		}
	}
	if def != (notify.Config{}) {
		n, err := disp.FillOpenWA(ctx, def)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Info("moved the old OpenWA defaults into the WhatsApp channels", "channels", n)
		}
	}
	if have {
		if err := st.DeleteSetting(ctx, "openwa"); err != nil {
			return err
		}
	}
	return st.SetSetting(ctx, openwaMoved, true)
}

// openBox returns the box for stored secrets. ATLANTIS_SECRET_KEY wins;
// otherwise the key lives in secret.key next to the database and is
// created on first start. It is never created for a database that already
// holds encrypted data, and whichever key is used must open that data.
func openBox(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) (*secret.Box, string, error) {
	box, source, err := chooseKey(ctx, cfg, st, log)
	if err != nil {
		return nil, "", err
	}
	sample, err := st.SealedSample(ctx)
	if err != nil {
		return nil, "", err
	}
	if _, err := box.Open(sample); err != nil {
		return nil, "", fmt.Errorf("the encryption key from %s does not match the passwords stored in %s; "+
			"start Atlantis with the key this database was set up with", source, cfg.StorePath)
	}
	return box, source, nil
}

func chooseKey(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) (*secret.Box, string, error) {
	if cfg.SecretKey != "" {
		box, err := secret.New(cfg.SecretKey)
		return box, "environment (ATLANTIS_SECRET_KEY)", err
	}
	path := filepath.Join(filepath.Dir(cfg.StorePath), secret.KeyFileName)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	sealed, err := st.HasSealed(ctx)
	if err != nil {
		return nil, "", err
	}
	key, created, err := secret.LoadKeyFile(path, !sealed)
	if errors.Is(err, secret.ErrNoKeyFile) {
		return nil, "", fmt.Errorf("%s holds encrypted passwords, but there is no ATLANTIS_SECRET_KEY and no %s: "+
			"set ATLANTIS_SECRET_KEY to the key this database was used with", cfg.StorePath, path)
	}
	if err != nil {
		return nil, "", err
	}
	if created {
		log.Warn("created a new secret key; back it up together with the database", "path", path)
	} else {
		log.Info("using the secret key file", "path", path)
	}
	box, err := secret.New(key)
	return box, path, err
}

// oldDefaultStore was the default database path before it became relative
// to the working directory.
const oldDefaultStore = "/data/atlantis.db"

// legacyStorePath keeps an install that relied on the old default working:
// with the new default and no database in the working directory, an
// existing database at the old default path is used.
func legacyStorePath(path string, log *slog.Logger) string {
	if path != config.DefaultStorePath {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if _, err := os.Stat(oldDefaultStore); err == nil {
		log.Info("using the existing database at the old default path; set ATLANTIS_STORE_PATH to choose", "path", oldDefaultStore)
		return oldDefaultStore
	}
	return path
}
