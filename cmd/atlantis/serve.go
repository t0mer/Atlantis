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

func newLogger(c config.Log, w io.Writer) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(c.Level))
	opts := &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
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

	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.Store.Path, err)
	}
	defer st.Close()

	// A Deco given in the configuration is stored as the site, as before;
	// without one, it is added from the UI.
	if cfg.Deco.Host != "" {
		sealed := ""
		if cfg.Deco.Password != "" {
			if sealed, err = box.Seal(cfg.Deco.Password); err != nil {
				return err
			}
		}
		if _, err := st.EnsureNetwork(ctx, store.Network{Host: cfg.Deco.Host, Port: cfg.Deco.Port, Transport: cfg.Deco.Transport, Email: cfg.Deco.Email,
			PasswordEnc: sealed, HostKey: cfg.Deco.HostKey, TLSPin: cfg.Deco.TLSPin}); err != nil {
			return err
		}
	}

	var m *metrics.Metrics
	var metricsHandler http.Handler
	if cfg.Metrics.Enabled {
		m = metrics.New(cfg.Metrics.PerClient)
		metricsHandler = m.Handler()
	}
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
	sites = site.New(ctx, site.Deps{Config: cfg, Store: st, Box: box, Hub: hub, Notify: disp, Metrics: m, Log: log})
	if err := sites.LoadPoll(ctx); err != nil {
		return err
	}
	if err := sites.Start(ctx); err != nil {
		// Keep serving so the connection can be fixed from the UI.
		log.Error("starting the Deco connection", "err", err)
	}
	defer sites.Close(context.Background())

	srv := api.New(api.Deps{Config: cfg, Store: st, Site: sites, Hub: hub, Notify: disp,
		Metrics: metricsHandler, Web: web.Handler(), Version: version, Log: log})
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
