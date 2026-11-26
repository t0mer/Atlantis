package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t0mer/atlantis/internal/store"
)

const (
	cookieName     = "atlantis_session"
	csrfHeader     = "X-CSRF-Token"
	sessionTTL     = 7 * 24 * time.Hour
	maxFailures    = 5
	failureWindow  = 15 * time.Minute
	minPasswordLen = 6
)

type ctxKey int

const userKey ctxKey = 0

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// limiter refuses logins after repeated failures for a username or address.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

func (l *limiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := l.now().Add(-failureWindow)
	for _, k := range keys {
		n := 0
		for _, t := range l.hits[k] {
			if t.After(cut) {
				n++
			}
		}
		if n >= maxFailures {
			return true
		}
	}
	return false
}

func (l *limiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		l.hits[k] = append(l.hits[k], l.now())
	}
}

func (l *limiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.hits, k)
	}
}

// dummyHash is compared against for unknown usernames.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("atlantis-no-such-user"), bcrypt.DefaultCost)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"setup_required": n == 0}
	if ss, ok := s.currentSession(r); ok {
		out["user"], out["csrf_token"] = ss.Username, ss.CSRF
	}
	writeJSON(w, http.StatusOK, out)
}

// setup creates the first (admin) account. It works exactly once.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	c.Username = strings.TrimSpace(c.Username)
	if c.Username == "" || len(c.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "username required and password of at least 6 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		s.fail(w, err)
		return
	}
	u, err := s.store.CreateFirstUser(r.Context(), c.Username, string(hash))
	if errors.Is(err, store.ErrUserExists) {
		writeError(w, http.StatusConflict, "setup is already done")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r.Context(), u.Username, "auth.setup", u.Username, nil)
	s.startSession(w, r, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	keys := []string{"user:" + strings.ToLower(c.Username), "ip:" + clientIP(r)}
	if s.limiter.blocked(keys...) {
		writeError(w, http.StatusTooManyRequests, "too many failed logins; try again later")
		return
	}
	// Count the attempt before the slow hash so concurrent guesses cannot
	// all pass the check; a success clears it again.
	s.limiter.fail(keys...)
	u, err := s.store.UserByName(r.Context(), c.Username)
	hash := []byte(u.PasswordHash)
	if err != nil {
		hash = dummyHash // same work for unknown users, so timing reveals nothing
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(c.Password)) != nil || err != nil {
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	s.limiter.reset(keys...)
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) {
	tok, csrf := randomToken(), randomToken()
	exp := s.now().Add(sessionTTL)
	if err := s.store.CreateSession(r.Context(), store.Session{TokenHash: hashToken(tok), UserID: u.ID, CSRF: csrf, ExpiresAt: exp}); err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secureCookies(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Username, "csrf_token": csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secureCookies(r)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) currentSession(r *http.Request) (store.Session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return store.Session{}, false
	}
	ss, err := s.store.GetSession(r.Context(), hashToken(c.Value))
	return ss, err == nil
}

// requireAuth admits requests with a valid session; state-changing
// methods must also carry the session's CSRF token.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ss, ok := s.currentSession(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			got := r.Header.Get(csrfHeader)
			if subtle.ConstantTimeCompare([]byte(got), []byte(ss.CSRF)) != 1 {
				writeError(w, http.StatusForbidden, "missing or wrong CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, ss.Username)))
	})
}

func userFrom(ctx context.Context) string {
	u, _ := ctx.Value(userKey).(string)
	return u
}

// clientIP is the connection's address; proxies are not trusted here.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}
