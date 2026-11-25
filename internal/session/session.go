// Package session owns the one admin session Atlantis keeps with the site's
// main Deco (CLAUDE.md §5.1). Every device call goes through Do, which
// serialises calls and turns failures into states the UI can show:
//
//   - a rejected password marks the session auth_failed and stops all
//     calls until the password is changed (no retries: the Deco locks out);
//   - the Deco ending the session because another admin (usually the Deco
//     app) holds its limited slots backs off exponentially, 30s to 10m;
//   - a changed or unknown host key/certificate stops until accepted;
//   - Pause and Release let the user hand the session to the phone app.
package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
)

// State is the session state shown in the UI banner.
type State string

const (
	StateIdle         State = "idle" // not connected yet
	StateConnected    State = "connected"
	StatePaused       State = "paused"
	StateBlocked      State = "blocked_by_other_admin"
	StateAuthFailed   State = "auth_failed"
	StateCertChanged  State = "cert_changed"
	StateCertUnknown  State = "cert_unknown"
	StateDisconnected State = "disconnected" // transient error; the next poll retries
)

// Status is a snapshot of the session.
type Status struct {
	State     State     `json:"state"`
	Since     time.Time `json:"since"`
	Error     string    `json:"error,omitempty"`
	RetryAt   time.Time `json:"retry_at,omitzero"`
	LastOK    time.Time `json:"last_ok,omitzero"`
	Logins    int       `json:"logins"`
	PausedFor string    `json:"paused_for,omitempty"`
}

// Errors returned by Do without contacting the device.
var (
	ErrPaused  = errors.New("session: paused")
	ErrBlocked = errors.New("session: waiting for another admin to release the Deco")
	ErrStopped = errors.New("session: stopped until credentials or the host key are fixed")
)

const (
	minBackoff = 30 * time.Second
	maxBackoff = 10 * time.Minute
)

// Manager wraps one deco.Client.
type Manager struct {
	client   deco.Client
	now      func() time.Time
	onChange func(Status)

	call     sync.Mutex // serialises device calls
	mu       sync.Mutex // guards the fields below
	st       Status
	backoff  time.Duration
	resumeAt time.Time // auto-resume after Release
	// stop latches auth_failed / cert_changed / cert_unknown. Pause,
	// Release and Resume never clear it: a rejected password is never sent
	// again (a new password needs a restart), and a key change is cleared
	// only by ClearCert after the user accepts the new key.
	stop  State
	stopE string
}

// New returns a manager. onChange, if non-nil, is called (without locks
// held) after every state change.
func New(c deco.Client, onChange func(Status)) *Manager {
	m := &Manager{client: c, now: time.Now, onChange: onChange}
	m.st = Status{State: StateIdle, Since: m.now()}
	return m
}

// SetClock replaces the time source (tests).
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// Client is the device client the manager guards. Use Do for device
// calls; this is for static capabilities (such as deco.WiFiWriter.Editable).
func (m *Manager) Client() deco.Client { return m.client }

// Status returns the current status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *Manager) set(state State, err error, mutate func(*Status)) {
	m.mu.Lock()
	changed := m.st.State != state || (err != nil) != (m.st.Error != "")
	if m.st.State != state {
		m.st.Since = m.now()
	}
	m.st.State = state
	m.st.Error = ""
	if err != nil {
		m.st.Error = err.Error()
	}
	if mutate != nil {
		mutate(&m.st)
	}
	st := m.st
	m.mu.Unlock()
	if changed && m.onChange != nil {
		m.onChange(st)
	}
}

// gate reports why a call may not run now, or nil.
func (m *Manager) gate() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.stop != "" {
		return ErrStopped
	}
	switch m.st.State {
	case StatePaused:
		if !m.resumeAt.IsZero() && !now.Before(m.resumeAt) {
			// Release period over: leave the paused state for good.
			m.resumeAt = time.Time{}
			m.st.State, m.st.Since, m.st.PausedFor = StateIdle, now, ""
			return nil // (a latched stop was already refused above)
		}
		return ErrPaused
	case StateBlocked:
		if now.Before(m.st.RetryAt) {
			return ErrBlocked
		}
	}
	return nil
}

// Do runs fn with the client, serialised with every other call.
func (m *Manager) Do(ctx context.Context, fn func(context.Context, deco.Client) error) error {
	if err := m.gate(); err != nil {
		return err
	}
	m.call.Lock()
	defer m.call.Unlock()
	if err := m.gate(); err != nil { // state may have changed while waiting
		return err
	}
	before := m.Status().State
	err := fn(ctx, m.client)
	m.classify(before, err)
	return err
}

func (m *Manager) classify(before State, err error) {
	switch {
	case err == nil:
		m.mu.Lock()
		m.backoff = 0
		m.mu.Unlock()
		m.set(StateConnected, nil, func(s *Status) {
			s.LastOK, s.RetryAt = m.now(), time.Time{}
			if before != StateConnected {
				s.Logins++
			}
		})
	case errors.Is(err, deco.ErrWrongPassword), errors.Is(err, deco.ErrAuthFailed):
		m.latch(StateAuthFailed, err)
		m.logout()
	case errors.Is(err, deco.ErrCertChanged):
		m.latch(StateCertChanged, err)
		m.logout()
	case errors.Is(err, deco.ErrCertUnknown):
		m.latch(StateCertUnknown, err)
	case errors.Is(err, deco.ErrOtherAdmin):
		m.mu.Lock()
		if m.backoff == 0 {
			m.backoff = minBackoff
		} else {
			m.backoff = min(2*m.backoff, maxBackoff)
		}
		retry := m.now().Add(m.backoff)
		m.mu.Unlock()
		m.set(StateBlocked, err, func(s *Status) { s.RetryAt = retry })
		m.logout()
	case errors.Is(err, deco.ErrUnsupported), errors.Is(err, context.Canceled):
		// a missing feature or shutdown says nothing about the session
	default:
		m.set(StateDisconnected, err, nil)
	}
}

func (m *Manager) latch(st State, err error) {
	m.mu.Lock()
	m.stop, m.stopE = st, err.Error()
	m.mu.Unlock()
	m.set(st, err, nil)
}

// restoreStop re-applies a latched stop after a pause or release ends.
func (m *Manager) restoreStop() bool {
	m.mu.Lock()
	st, e := m.stop, m.stopE
	m.mu.Unlock()
	if st == "" {
		return false
	}
	m.set(st, errors.New(e), func(s *Status) { s.RetryAt, s.PausedFor = time.Time{}, "" })
	return true
}

func (m *Manager) logout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.client.Logout(ctx)
}

// Pause stops all device calls and releases the Deco's admin session.
func (m *Manager) Pause(ctx context.Context) error {
	m.call.Lock()
	defer m.call.Unlock()
	m.mu.Lock()
	m.resumeAt = time.Time{}
	m.mu.Unlock()
	err := m.client.Logout(ctx)
	if m.Stopped() == "" {
		m.set(StatePaused, nil, func(s *Status) { s.PausedFor = "" })
	}
	return err
}

// Release logs out and pauses for d, so the Deco app can be used; calls
// resume by themselves afterwards.
func (m *Manager) Release(ctx context.Context, d time.Duration) error {
	err := m.Pause(ctx)
	if m.Stopped() != "" {
		return err // nothing to resume into
	}
	m.mu.Lock()
	m.resumeAt = m.now().Add(d)
	m.st.PausedFor = d.String()
	m.mu.Unlock()
	return err
}

// Resume allows calls again after Pause, Release or a backoff. It does not
// clear auth_failed (restart with a new password) or a certificate stop
// (ClearCert).
func (m *Manager) Resume() {
	m.mu.Lock()
	m.resumeAt, m.backoff = time.Time{}, 0
	m.mu.Unlock()
	if m.restoreStop() {
		return
	}
	m.set(StateIdle, nil, func(s *Status) { s.RetryAt, s.PausedFor = time.Time{}, "" })
}

// ClearCert lifts a cert_changed / cert_unknown stop after the user has
// accepted the device's current key. It never clears auth_failed.
func (m *Manager) ClearCert() {
	m.mu.Lock()
	if m.stop == StateCertChanged || m.stop == StateCertUnknown {
		m.stop, m.stopE = "", ""
	}
	m.mu.Unlock()
	m.Resume()
}

// Stopped reports the latched stop state, or "".
func (m *Manager) Stopped() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stop
}

// Close logs out.
func (m *Manager) Close(ctx context.Context) error {
	m.call.Lock()
	defer m.call.Unlock()
	return m.client.Logout(ctx)
}
