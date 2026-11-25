package api

import (
	"errors"
	"net/http"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/site"
)

// decoPasswordKey rate-limits rejected Deco passwords, so the UI cannot
// get the TP-Link ID locked by repeated wrong attempts.
const decoPasswordKey = "deco-password"

type decoView struct {
	Configured    bool            `json:"configured"`
	Running       bool            `json:"running"`
	Changing      bool            `json:"changing"` // a change is being checked; the old connection is stopped meanwhile
	NeedsPassword bool            `json:"needs_password"`
	Host          string          `json:"host,omitempty"`
	Port          int             `json:"port,omitempty"`
	Transport     string          `json:"transport,omitempty"`
	Email         string          `json:"email,omitempty"`
	HostKey       string          `json:"host_key,omitempty"`
	Discovery     *site.Discovery `json:"discovery,omitempty"`
}

func (s *Server) decoStatus(w http.ResponseWriter, r *http.Request) {
	n, ok, err := s.store.GetNetwork(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	v := decoView{Configured: ok, Changing: s.Site.Changing()}
	if ok {
		v.Host, v.Port, v.Transport, v.Email, v.HostKey = n.Host, n.Port, n.Transport, n.Email, n.HostKey
		v.NeedsPassword = n.PasswordEnc == "" || n.Host == ""
	}
	if run := s.Site.Current(); run != nil {
		v.Running, v.Discovery = true, run.Discovery
	}
	writeJSON(w, http.StatusOK, v)
}

// connectDeco adds the Deco (POST) or changes its connection (PUT). The
// settings are only stored after a successful login.
func (s *Server) connectDeco(w http.ResponseWriter, r *http.Request) {
	var c site.Conn
	if !readJSON(w, r, &c) {
		return
	}
	_, have, err := s.store.GetNetwork(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	action := "deco.add"
	if r.Method == http.MethodPut {
		action = "deco.change"
		if !have {
			writeJSON(w, http.StatusConflict, map[string]string{"error": site.ErrNoDeco.Error(), "code": "no_deco"})
			return
		}
	} else if have && s.Site.Current() != nil {
		writeError(w, http.StatusConflict, "a Deco is already connected; change or remove it in Settings")
		return
	}
	if s.limiter.blocked(decoPasswordKey) {
		writeError(w, http.StatusTooManyRequests, "the Deco rejected the password too many times; wait 15 minutes so the TP-Link ID is not locked")
		return
	}
	n, err := s.Site.Connect(r.Context(), c)
	s.audit(r.Context(), userFrom(r.Context()), action, c.Host, err)
	if err != nil {
		s.connectFailed(w, err)
		return
	}
	s.limiter.reset(decoPasswordKey)
	v := decoView{Configured: true, Running: true, Host: n.Host, Port: n.Port, Transport: n.Transport, Email: n.Email, HostKey: n.HostKey}
	if run := s.Site.Current(); run != nil {
		v.Discovery = run.Discovery
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) connectFailed(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, site.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, deco.ErrWrongPassword):
		s.limiter.fail(decoPasswordKey)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, site.ErrTLSOnly), errors.Is(err, site.ErrNotMain), errors.Is(err, deco.ErrOtherAdmin), errors.Is(err, deco.ErrCertChanged):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, site.ErrUnreachable):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		s.fail(w, err)
	}
}

// removeDeco disconnects and deletes the Deco with its history.
func (s *Server) removeDeco(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Confirm != "REMOVE" {
		writeError(w, http.StatusBadRequest, `type "REMOVE" to confirm; this deletes the Deco's history`)
		return
	}
	n, _, _ := s.store.GetNetwork(r.Context())
	err := s.Site.Remove(r.Context())
	s.audit(r.Context(), userFrom(r.Context()), "deco.remove", n.Host, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
