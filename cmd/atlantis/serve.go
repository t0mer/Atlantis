package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/luci"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn"
	"github.com/t0mer/atlantis/internal/deco/tmp/tdp"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/session"
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

// hostKeys keeps the SSH host-key pin in the store and lets the API accept a
// new key: after Accept, the next connection trusts and stores whatever key
// the Deco presents.
type hostKeys struct {
	mu        sync.Mutex
	pin       string
	acceptNew bool
	save      func(string) error
}

func (h *hostKeys) current() (pin string, acceptNew bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pin, h.acceptNew
}

func (h *hostKeys) seen(fp string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.acceptNew {
		h.pin, h.acceptNew = fp, false
		_ = h.save(fp)
	}
}

func (h *hostKeys) accept() {
	h.mu.Lock()
	h.pin, h.acceptNew = "", true
	h.mu.Unlock()
}

func serve(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.Store.Path, err)
	}
	defer st.Close()

	sealed := ""
	if cfg.Deco.Password != "" {
		if sealed, err = box.Seal(cfg.Deco.Password); err != nil {
			return err
		}
	}
	netw, err := st.EnsureNetwork(ctx, store.Network{Host: cfg.Deco.Host, Transport: cfg.Deco.Transport, Email: cfg.Deco.Email,
		PasswordEnc: sealed, HostKey: cfg.Deco.HostKey, TLSPin: cfg.Deco.TLSPin})
	if err != nil {
		return err
	}
	password, err := box.Open(netw.PasswordEnc)
	if err != nil {
		return err
	}
	if password == "" {
		return errors.New("no Deco password: set deco.password (or ATLANTIS_DECO_PASSWORD) once; it is then kept encrypted")
	}

	keys := &hostKeys{pin: netw.HostKey, save: func(fp string) error { return st.SetHostKey(context.Background(), netw.ID, fp) }}
	if keys.pin == "" {
		keys.acceptNew = true // first start: trust and store the key the Deco presents (TOFU)
	}
	client, err := newDecoClient(ctx, cfg, password, keys, netw.TLSPin, log)
	if err != nil {
		return err
	}

	var m *metrics.Metrics
	var metricsHandler http.Handler
	if cfg.Metrics.Enabled {
		m = metrics.New(cfg.Metrics.PerClient)
		metricsHandler = m.Handler()
	}
	hub := poller.NewHub()
	var lastState session.State
	sess := session.New(client, func(s session.Status) {
		hub.Publish(poller.Message{Kind: "session", Data: s})
		if m != nil && s.State == session.StateConnected && lastState != session.StateConnected {
			m.Login("ok")
		} else if m != nil && s.State == session.StateAuthFailed {
			m.Login("auth_failed")
		}
		lastState = s.State
		log.Info("deco session", "state", s.State, "error", s.Error)
	})
	defer sess.Close(context.Background())

	var p *poller.Poller
	siteName := func() string {
		if p != nil {
			for _, n := range p.Snapshot().Nodes {
				if n.Master && n.Nickname != "" {
					return n.Nickname
				}
			}
		}
		return cfg.Deco.Host
	}
	disp := notify.New(st, box, cfg, siteName, log)
	p, err = poller.New(ctx, poller.Options{NetworkID: netw.ID, Fast: cfg.Poll.Fast, Slow: cfg.Poll.Slow, OfflineAfter: cfg.Poll.OfflineAfterMisses,
		RawRetention: cfg.Store.RawRetention, RollupRetention: cfg.Store.RollupRetention}, sess, st, disp, m, hub, log)
	if err != nil {
		return err
	}

	srv := api.New(api.Deps{Config: cfg, Store: st, NetworkID: netw.ID, Session: sess, Poller: p, Hub: hub, Notify: disp,
		Metrics: metricsHandler, Web: web.Handler(), Version: version, Log: log,
		AcceptHostKey: func(context.Context) error {
			keys.accept()
			return st.SetHostKey(context.Background(), netw.ID, "")
		}})
	httpSrv := &http.Server{Addr: cfg.Server.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); disp.Run(ctx) }()
	go func() { defer wg.Done(); p.Run(ctx) }()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Server.Listen, "deco", cfg.Deco.Host, "transport", cfg.Deco.Transport, "version", version)
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

// newDecoClient builds the transport's client: SSH (TDP discovery, TP-Link
// ID login, host-key pinning) or the local web API.
// The first SSH connection trusts and stores the key the Deco presents
// (trust on first use, CLAUDE.md §5.1); later a different key stops the
// session until it is accepted through the API. deco.host_key pins one up
// front.
func newDecoClient(ctx context.Context, cfg *config.Config, password string, keys *hostKeys, tlsPin string, log *slog.Logger) (deco.Client, error) {
	switch cfg.Deco.Transport {
	case "luci":
		base := cfg.Deco.Host
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		return luci.New(base, password, luci.Options{Pin: tlsPin, OnFingerprint: func(fp string) {
			log.Warn("trusting the Deco's TLS certificate on first use; pin it with deco.tls_pin", "fingerprint", fp)
		}})
	}

	port, digest, tps := cfg.Deco.Port, false, false
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	info, err := tdp.Discover(dctx, cfg.Deco.Host)
	cancel()
	if err != nil {
		log.Warn("TDP discovery failed; using defaults", "err", err)
	} else {
		digest, tps = info.NeedAccountDigest, info.TPSBanner()
		if port == 0 {
			port = info.SSHPort()
		}
		log.Info("deco discovered", "model", info.Model, "role", info.Role, "ssh_port", info.SSHPort())
	}
	if port == 0 {
		port = 20001
	}
	user := sshconn.Username(cfg.Deco.Email, digest, false)
	dial := func(ctx context.Context, _ func(string, ...any)) (io.ReadWriteCloser, error) {
		pin, acceptNew := keys.current()
		return sshconn.Dial(ctx, sshconn.Options{
			Host: cfg.Deco.Host, Port: port, User: user, Password: password, TPSBanner: tps,
			HostKeyPin: pin, AcceptNew: acceptNew, OnHostKey: func(fp string) {
				log.Info("ssh host key", "fingerprint", fp, "trusted", acceptNew)
				keys.seen(fp)
			},
		})
	}
	return tmpclient.New(dial, tmp.Options{Username: cfg.Deco.Email, Owner: true, Heartbeat: 3 * time.Second}), nil
}
