// Package api serves the REST API under /api/v1 (documented in
// api/openapi.yaml, Swagger UI at /api/docs), the live event stream, the
// Prometheus metrics and the embedded web UI. Every route except /health,
// the auth endpoints and the UI requires a login; mutations also need the
// session's CSRF token.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/prefs"
	"github.com/t0mer/atlantis/internal/session"
	"github.com/t0mer/atlantis/internal/site"
	"github.com/t0mer/atlantis/internal/store"
)

// Deps are the server's collaborators.
type Deps struct {
	Config  *config.Config
	Store   *store.Store
	Site    *site.Site // the Deco connection; may have nothing running
	Hub     *poller.Hub
	Notify  *notify.Dispatcher
	Prefs   *prefs.Prefs // settings edited in the UI (metrics, access, debug, ...)
	Startup Startup      // settings fixed at startup, shown read-only
	Metrics http.Handler // the Prometheus handler; served while enabled in Prefs
	Web     http.Handler // the embedded UI; nil for none
	Version string
	Log     *slog.Logger
}

type runKey struct{}

// runOf returns the Deco connection needDeco put in the request context.
func runOf(ctx context.Context) *site.Run { return ctx.Value(runKey{}).(*site.Run) }

// needDeco answers 409 when no Deco is connected, so the UI can offer to
// add one, and otherwise passes the current connection to the handler.
func (s *Server) needDeco(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run := s.Site.Current()
		if run == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": site.ErrNoDeco.Error(), "code": "no_deco"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runKey{}, run)))
	})
}

// Server is the HTTP API.
type Server struct {
	Deps
	store   *store.Store
	limiter *limiter
	now     func() time.Time
	started time.Time
}

// New builds the server.
func New(d Deps) *Server {
	s := &Server{Deps: d, store: d.Store, now: time.Now, started: time.Now(),
		limiter: &limiter{hits: map[string][]time.Time{}, now: time.Now}}
	return s
}

// secureCookies marks the session cookie Secure when Atlantis is reached
// over HTTPS (the public URL set in Settings).
func (s *Server) secureCookies() bool {
	return strings.HasPrefix(s.Prefs.Get().Access.BaseURL, "https://")
}

// Handler returns the router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, securityHeaders, s.metricsRoute)
	r.Get("/health", s.health)
	r.Get("/api/docs", docsPage)
	r.Get("/api/openapi.yaml", openAPISpec)
	r.Route("/api/v1", func(r chi.Router) {
		// The live stream runs indefinitely, so it sits outside the timeout.
		r.With(s.requireAuth).Get("/stream", s.stream)
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(60 * time.Second))
			r.Get("/auth/status", s.authStatus)
			r.Post("/auth/setup", s.setup)
			r.Post("/auth/login", s.login)
			r.Post("/auth/logout", s.logout)
			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)
				r.Get("/settings", s.settings)
				r.Put("/settings", s.putSettings)
				r.Get("/channels", s.listChannels)
				r.Post("/channels", s.createChannel)
				r.Put("/channels/{id}", s.updateChannel)
				r.Delete("/channels/{id}", s.deleteChannel)
				r.Post("/channels/{id}/test", s.testChannel)
				r.Post("/channels/test", s.testDraftChannel)
				r.Get("/audit", s.auditLog)
				r.Get("/deco", s.decoStatus)
				r.Post("/deco", s.connectDeco)
				r.Put("/deco", s.connectDeco)
				r.Delete("/deco", s.removeDeco)
			})
			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth, s.needDeco)
				r.Get("/ready", s.ready)
				r.Get("/network", s.network)
				r.Get("/nodes", s.nodes)
				r.Get("/nodes/{id}", s.node)
				r.Post("/nodes/{id}/reboot", notVerified)
				r.Post("/nodes/reboot-all", notVerified)
				r.Get("/clients", s.clients)
				r.Get("/clients/{mac}", s.client)
				r.Patch("/clients/{mac}", s.patchClient)
				r.Get("/wifi", s.wifi)
				r.Patch("/wifi", s.patchWiFi)
				r.Get("/firmware", s.firmware)
				r.Get("/events", s.events)
				r.Get("/session", s.sessionStatus)
				r.Post("/session/logout", s.releaseSession)
				r.Post("/session/pause", s.pauseSession)
				r.Post("/session/resume", s.resumeSession)
				r.Post("/session/accept-host-key", s.acceptHostKey)
				r.Get("/debug/raw", s.debugRaw) // answers 404 unless enabled in Settings
			})
		})
	})
	if s.Web != nil {
		r.Handle("/*", s.Web)
	}
	return r
}

// metricsAuth admits a matching bearer token (for Prometheus) when
// metrics.token is set, and otherwise a logged-in session.
// metricsRoute serves Prometheus metrics at the path set in Settings while
// they are enabled; the path can change at runtime, so it is matched here
// rather than registered in the router.
func (s *Server) metricsRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := s.Prefs.Get().Metrics; s.Metrics != nil && m.Enabled && r.URL.Path == m.Path {
			// Metrics carry client names and MACs: never public.
			s.metricsAuth(s.Metrics).ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) metricsAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := s.Prefs.Get().Metrics.Token; tok != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if ok && subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		if _, ok := s.currentSession(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="atlantis-metrics"`)
		writeError(w, http.StatusUnauthorized, "metrics need a bearer token or a login")
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// readJSON decodes a small JSON body, rejecting unknown fields.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	// Requiring a JSON content type makes cross-site form posts (text/plain
	// bodies that happen to parse as JSON) fail, which matters for the
	// routes without a CSRF token: first-run setup and login.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// fail answers a server-side error without leaking details to the client.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, session.ErrClosed):
		writeError(w, http.StatusConflict, "the Deco connection changed while this was running; try again")
	case errors.Is(err, session.ErrPaused), errors.Is(err, session.ErrBlocked), errors.Is(err, session.ErrStopped):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, deco.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, "not supported by this Deco")
	case errors.Is(err, deco.ErrNotVerified):
		writeError(w, http.StatusNotImplemented, "not available yet: "+err.Error())
	case errors.Is(err, site.ErrNoDeco):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "no_deco"})
	default:
		s.Log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) audit(ctx context.Context, user, action, target string, err error) {
	a := store.Audit{User: user, Action: action, Target: target, Result: "ok"}
	if err != nil {
		a.Result, a.Error = "error", err.Error()
	}
	if aerr := s.store.AddAudit(ctx, a); aerr != nil {
		s.Log.Error("audit log", "err", aerr)
	}
}

// notVerified answers writes that are not confirmed on hardware yet
// (CLAUDE.md §12.2): they are documented but not sent to the Deco.
func notVerified(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not available yet: this action has not been verified on a Deco")
}
