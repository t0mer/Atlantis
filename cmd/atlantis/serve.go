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
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Atlantis server: poll the Deco, keep history, serve the API and UI",
		Long: `Runs Atlantis for one Deco site on the LAN. Settings come from flags, then
ATLANTIS_* environment variables, then the YAML file given with --config (or
ATLANTIS_CONFIG). ATLANTIS_SECRET_KEY is required: generate it once with
"atlantis keygen" and keep it, or stored secrets cannot be read.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cfgPath == "" {
				cfgPath = os.Getenv("ATLANTIS_CONFIG")
			}
			cfg, err := config.Load(cfgPath, cmd.Flags())
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serve(ctx, cfg, newLogger(cfg.Log, os.Stderr))
		},
	}
	f := cmd.Flags()
	f.StringVar(&cfgPath, "config", "", "YAML configuration file (or ATLANTIS_CONFIG)")
	f.String("server-listen", "", "listen address, e.g. :8080")
	f.String("deco-host", "", "main Deco IP address")
	f.String("deco-transport", "", "ssh (default) or luci")
	f.String("deco-email", "", "TP-Link ID email (ssh)")
	f.String("store-path", "", "SQLite database path")
	f.String("log-level", "", "debug, info, warn or error")
	return cmd
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

// logLevel is the process's log level; Settings can change it at runtime.
var logLevel = new(slog.LevelVar)

// setLogLevel applies a level name (debug, info, warn, error).
func setLogLevel(name string) {
	var lvl slog.Level
	if lvl.UnmarshalText([]byte(name)) == nil {
		logLevel.Set(lvl)
	}
}

func newLogger(c config.Log, w io.Writer) *slog.Logger {
	setLogLevel(c.Level)
	opts := &slog.HandlerOptions{Level: logLevel, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if secretAttr(a.Key) {
			return slog.String(a.Key, "[redacted]")
		}
		return a
	}}
	if c.Format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func serve(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	// Shoutrrr uses http.DefaultClient, which has no timeout; bound it so an
	// abandoned notification send cannot hang for ever.
	http.DefaultClient.Timeout = 30 * time.Second

	st, err := store.Open(ctx, cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.Store.Path, err)
	}
	defer st.Close()
	box, keySource, err := openBox(ctx, cfg, st, log)
	if err != nil {
		return err
	}

	if err := seedDeco(ctx, cfg, st, box, log); err != nil {
		return err
	}

	// Settings edited in the UI; the configuration only gives defaults.
	pf := prefs.New(cfg, st, box)
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
	disp := notify.New(st, box, cfg, siteName, log)
	pf.OnApply(func(v prefs.Values) {
		disp.SetPrefs(v.Notify, v.OpenWA)
		m.SetPerClient(v.Metrics.PerClient)
		setLogLevel(v.Logging.Level)
	})
	retention := func() (time.Duration, time.Duration) {
		h := pf.Get().History
		return h.RawRetention, h.RollupRetention
	}
	sites = site.New(ctx, site.Deps{Config: cfg, Store: st, Box: box, Hub: hub, Notify: disp, Metrics: m, Log: log, Retention: retention})
	if err := sites.LoadPoll(ctx); err != nil {
		return err
	}
	if err := sites.Start(ctx); err != nil {
		// Keep serving so the connection can be fixed from the UI.
		log.Error("starting the Deco connection", "err", err)
	}
	defer sites.Close(context.Background())

	dataPath := cfg.Store.Path
	if abs, err := filepath.Abs(dataPath); err == nil {
		dataPath = abs
	}
	startup := api.Startup{Listen: cfg.Server.Listen, DataPath: dataPath, KeySource: keySource, LogFormat: cfg.Log.Format}
	srv := api.New(api.Deps{Config: cfg, Store: st, Site: sites, Prefs: pf, Startup: startup, Hub: hub, Notify: disp,
		Metrics: m.Handler(), Web: web.Handler(), Version: version, Log: log})
	// Request contexts end with ctx, so live streams stop on shutdown.
	httpSrv := &http.Server{Addr: cfg.Server.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
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
		log.Info("listening", "addr", cfg.Server.Listen, "deco", host, "version", version)
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

// seedDeco stores the Deco given in the configuration, if any, but only
// while none is stored: once it exists, the UI owns the connection, so a
// restart never undoes a change made there (or pairs one Deco's address
// with another's pinned key). The one exception is a stored Deco that still
// has no password (seeded without one): the configured password fills it in.
func seedDeco(ctx context.Context, cfg *config.Config, st *store.Store, box *secret.Box, log *slog.Logger) error {
	if cfg.Deco.Host == "" {
		return nil
	}
	cur, have, err := st.GetNetwork(ctx)
	if err != nil {
		return err
	}
	sealed := ""
	if cfg.Deco.Password != "" {
		if sealed, err = box.Seal(cfg.Deco.Password); err != nil {
			return err
		}
	}
	switch {
	case !have || cur.Host == "": // nothing stored, or a placeholder without an address
		n := store.Network{ID: cur.ID, Host: cfg.Deco.Host, Port: cfg.Deco.Port, Transport: cfg.Deco.Transport, Email: cfg.Deco.Email,
			PasswordEnc: sealed, HostKey: cfg.Deco.HostKey, TLSPin: cfg.Deco.TLSPin}
		_, err = st.SaveNetwork(ctx, n)
		return err
	case cur.PasswordEnc == "" && sealed != "" && cur.Host == cfg.Deco.Host:
		cur.PasswordEnc = sealed
		_, err = st.SaveNetwork(ctx, cur)
		return err
	case cur.Host != cfg.Deco.Host || cur.Email != cfg.Deco.Email:
		log.Warn("deco.* in the configuration is ignored: a Deco is already stored; change it in Settings", "stored", cur.Host, "configured", cfg.Deco.Host)
	}
	return nil
}

// openBox returns the box for stored secrets. ATLANTIS_SECRET_KEY wins;
// otherwise the key lives in secret.key next to the database and is
// created on first start. It is never created for a database that already
// holds encrypted data: that data needs the key it was sealed with.
func openBox(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) (box *secret.Box, keySource string, err error) {
	if cfg.SecretKey != "" {
		box, err = secret.New(cfg.SecretKey)
		return box, "environment (ATLANTIS_SECRET_KEY)", err
	}
	path := filepath.Join(filepath.Dir(cfg.Store.Path), secret.KeyFileName)
	sealed, err := st.HasSealed(ctx)
	if err != nil {
		return nil, "", err
	}
	key, created, err := secret.LoadKeyFile(path, !sealed)
	if errors.Is(err, secret.ErrNoKeyFile) {
		return nil, "", fmt.Errorf("%s holds encrypted passwords, but there is no ATLANTIS_SECRET_KEY and no %s: "+
			"set ATLANTIS_SECRET_KEY to the key this database was used with", cfg.Store.Path, path)
	}
	if err != nil {
		return nil, "", err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if created {
		log.Warn("created a new secret key; back it up together with the database", "path", path)
	} else {
		log.Info("using the secret key file", "path", path)
	}
	box, err = secret.New(key)
	return box, path, err
}
