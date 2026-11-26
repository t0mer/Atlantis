// Package site runs Atlantis's connection to the one Deco mesh it manages:
// the device client, its admin session and the poller, built from the site
// stored in the database. The site can be added, changed and removed at
// runtime (from the UI), so Atlantis also runs with no Deco at all.
package site

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/luci"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn"
	"github.com/t0mer/atlantis/internal/deco/tmp/tdp"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
	"github.com/t0mer/atlantis/internal/events"
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/prefs"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/session"
	"github.com/t0mer/atlantis/internal/store"
)

// Errors a connection attempt reports to the user.
var (
	ErrTLSOnly     = errors.New("this Deco only accepts TP-Link's newer encrypted (TLS) connection, which Atlantis does not support yet")
	ErrNotMain     = errors.New("this is a satellite Deco; enter the address of the main Deco (the one connected to the modem)")
	ErrUnreachable = errors.New("could not reach the Deco at that address")
	ErrNoDeco      = errors.New("no Deco connected")
	ErrInvalid     = errors.New("invalid connection settings")
)

// Conn are the settings for connecting to the main Deco.
type Conn struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`      // SSH port; 0 = discover
	Transport string `json:"transport"` // ssh (default) or luci
	Email     string `json:"email"`     // TP-Link ID, ssh only
	Password  string `json:"password"`  // empty on a change = keep the stored one
}

// Discovery is what the Deco said about itself (TDP), when it answered.
type Discovery struct {
	Model string `json:"model"`
	Role  string `json:"role"`
	Port  int    `json:"port"`
}

// Run is a running connection to the site.
type Run struct {
	Network   store.Network
	Session   *session.Manager
	Poller    *poller.Poller
	Discovery *Discovery

	keys   *hostKeys
	cancel context.CancelFunc
	done   chan struct{}
}

// Builder makes the device client for a site. keys keeps the SSH host-key
// pin; n.TLSPin and saveTLSPin do the same for the local web transport.
type Builder func(ctx context.Context, n store.Network, password string, keys *HostKeys, saveTLSPin func(string), log *slog.Logger) (deco.Client, *Discovery, error)

// Deps are the site's collaborators.
type Deps struct {
	Store   *store.Store
	Box     *secret.Box
	Hub     *poller.Hub
	Notify  poller.Notifier
	Metrics *metrics.Metrics // nil when off
	Log     *slog.Logger
	Build   Builder // nil = the real transports
	// Retention returns the current history retention (Settings); nil
	// keeps the defaults.
	Retention func() (raw, rollup time.Duration)
	// Poll is the refresh intervals until some are saved; zero = DefaultPoll.
	Poll Poll
	// TrustNewHostKey says whether a changed SSH host key is trusted
	// automatically (Settings); nil never does.
	TrustNewHostKey func() bool
}

// Site owns the current Run.
type Site struct {
	d    Deps
	root context.Context

	op       sync.Mutex // serialises Start, Connect, Remove and Close
	mu       sync.RWMutex
	run      *Run
	changing atomic.Bool // a stored Deco's connection is being changed

	pollMu sync.Mutex // serialises SetPoll
	poll   Poll
}

// New returns a site with nothing running. ctx bounds every run.
func New(ctx context.Context, d Deps) *Site {
	if d.Build == nil {
		d.Build = Build
	}
	poll := d.Poll
	if poll == (Poll{}) {
		poll = DefaultPoll
	}
	return &Site{d: d, root: ctx, poll: poll}
}

// Current returns the running connection, or nil when no Deco is connected.
func (s *Site) Current() *Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.run
}

// Start runs the stored site, if there is one with a password.
func (s *Site) Start(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	n, ok, err := s.d.Store.GetNetwork(ctx)
	if err != nil || !ok {
		return err
	}
	password, err := s.d.Box.Open(n.PasswordEnc)
	if err != nil {
		return err
	}
	if password == "" {
		s.d.Log.Warn("the Deco has no stored password; add it in the UI")
		return nil
	}
	return s.start(ctx, n, password, s.newHostKeys(n))
}

func (s *Site) start(ctx context.Context, n store.Network, password string, keys *hostKeys) error {
	// A run outlives the request that starts it: discovery and setup must
	// not fail because a browser tab closed or a request timed out.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	saveTLSPin := func(fp string) {
		if err := s.d.Store.SetTLSPin(context.Background(), n.ID, fp); err != nil {
			s.d.Log.Error("saving the TLS pin", "err", err)
		}
	}
	client, disc, err := s.d.Build(ctx, n, password, &HostKeys{k: keys}, saveTLSPin, s.d.Log)
	if err != nil {
		return err
	}
	var stateMu sync.Mutex
	var last session.State
	sess := session.New(client, func(st session.Status) {
		s.d.Hub.Publish(poller.Message{Kind: "session", Data: st})
		stateMu.Lock()
		prev := last
		last = st.State
		stateMu.Unlock()
		if m := s.d.Metrics; m != nil && st.State != prev {
			switch st.State {
			case session.StateConnected:
				m.Login("ok")
			case session.StateAuthFailed:
				m.Login("auth_failed")
			}
		}
		s.d.Log.Info("deco session", "state", st.State, "error", st.Error)
	})
	pl := s.Poll()
	h := prefs.Defaults().History
	p, err := poller.New(ctx, poller.Options{NetworkID: n.ID, Fast: pl.Fast, Slow: pl.Slow, OfflineAfter: pl.OfflineAfterMisses,
		RawRetention: h.RawRetention, RollupRetention: h.RollupRetention, Retention: s.d.Retention}, sess, s.d.Store, s.d.Notify, s.d.Metrics, s.d.Hub, s.d.Log)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithCancel(s.root)
	r := &Run{Network: n, Session: sess, Poller: p, Discovery: disc, keys: keys, cancel: cancel, done: make(chan struct{})}
	go func() { defer close(r.done); p.Run(rctx) }()
	s.mu.Lock()
	s.run = r
	now := s.poll
	s.mu.Unlock()
	if now != pl { // SetPoll ran while this run was being built
		p.SetIntervals(poller.Intervals{Fast: now.Fast, Slow: now.Slow, OfflineAfter: now.OfflineAfterMisses})
	}
	s.d.Hub.Publish(poller.Message{Kind: "deco", Data: nil})
	s.d.Log.Info("deco connected", "host", n.Host, "transport", n.Transport)
	return nil
}

// stop ends the current run: the poller exits, then the Deco session is
// logged out so the admin slot is free.
func (s *Site) stop(ctx context.Context) {
	s.mu.Lock()
	r := s.run
	s.run = nil
	s.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
	_ = r.Session.Close(ctx)
	s.d.Hub.Publish(poller.Message{Kind: "deco", Data: nil})
}

// Close stops the run (shutdown).
func (s *Site) Close(ctx context.Context) {
	s.op.Lock()
	defer s.op.Unlock()
	s.stop(ctx)
}

// Connect checks c by logging in to the Deco and reading its node list, and
// only then stores it and runs it. With a site already stored, c replaces
// its connection settings and the history is kept; an empty password keeps
// the stored one. The running connection is stopped during the check
// (the Deco allows few admin sessions) and restarted if the check fails.
func (s *Site) Connect(ctx context.Context, c Conn) (store.Network, error) {
	s.op.Lock()
	defer s.op.Unlock()
	c.Host = strings.TrimSpace(c.Host)
	c.Email = strings.TrimSpace(c.Email)
	if c.Transport == "" {
		c.Transport = "ssh"
	}
	if err := c.validate(); err != nil {
		return store.Network{}, err
	}
	cur, have, err := s.d.Store.GetNetwork(ctx)
	if err != nil {
		return store.Network{}, err
	}
	n := store.Network{Host: c.Host, Port: c.Port, Transport: c.Transport, Email: c.Email}
	password := c.Password
	if have {
		n.ID, n.Name = cur.ID, cur.Name
		sameTarget := cur.Host == n.Host && cur.Port == n.Port && cur.Transport == n.Transport && strings.EqualFold(cur.Email, n.Email)
		if password == "" {
			// The stored password is only ever sent to the address and
			// account it was entered for; a new target needs it typed again,
			// so the saved credential cannot be redirected to another
			// address. (With Security.TrustNewHostKey on, a changed SSH key
			// at the same address is trusted.)
			if !sameTarget {
				return store.Network{}, fmt.Errorf("%w: enter the password to connect to a different address or account", ErrInvalid)
			}
			if password, err = s.d.Box.Open(cur.PasswordEnc); err != nil {
				return store.Network{}, err
			}
		}
		if cur.Host == n.Host && cur.Transport == n.Transport { // same device: keep its pins
			n.HostKey, n.TLSPin = cur.HostKey, cur.TLSPin
		}
	}
	if password == "" {
		return store.Network{}, fmt.Errorf("%w: the password is required", ErrInvalid)
	}

	if have {
		s.changing.Store(true)
		defer s.changing.Store(false)
	}
	wasRunning := s.Current() != nil
	s.stop(ctx)
	fail := func(err error) (store.Network, error) {
		if wasRunning {
			s.restartStored(ctx)
		}
		return store.Network{}, err
	}
	changed, err := s.check(ctx, &n, password)
	if err != nil {
		return fail(err)
	}
	if n.PasswordEnc, err = s.d.Box.Seal(password); err != nil {
		return fail(err)
	}
	saved, err := s.d.Store.SaveNetwork(context.WithoutCancel(ctx), n)
	if err != nil {
		return fail(err)
	}
	if changed != nil {
		s.recordKeyChange(saved, changed.from, changed.to)
	}
	return saved, s.start(ctx, saved, password, s.newHostKeys(saved))
}

// Changing reports whether a stored Deco's connection is being changed
// (its run is stopped while the new settings are checked).
func (s *Site) Changing() bool { return s.changing.Load() }

// restartStored runs the site as stored again, after a change failed. It
// reads the store rather than the old run so pins learned meanwhile hold.
func (s *Site) restartStored(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	n, ok, err := s.d.Store.GetNetwork(ctx)
	if err != nil || !ok {
		s.d.Log.Error("restarting the previous Deco connection", "err", err)
		return
	}
	password, err := s.d.Box.Open(n.PasswordEnc)
	if err == nil {
		err = s.start(ctx, n, password, s.newHostKeys(n))
	}
	if err != nil {
		s.d.Log.Error("restarting the previous Deco connection", "err", err)
		s.d.Hub.Publish(poller.Message{Kind: "deco", Data: nil}) // nothing runs now: let the UI offer to fix it
	}
}

// keyChange is an SSH host key trusted in place of the pinned one.
type keyChange struct{ from, to string }

// check logs in to the Deco with n and reads its nodes, then logs out. A
// host key or certificate seen for the first time, or a changed host key
// trusted automatically, is recorded in n; the change is returned so it is
// recorded as an event once n is saved.
func (s *Site) check(ctx context.Context, n *store.Network, password string) (*keyChange, error) {
	var changed *keyChange
	keys := &hostKeys{pin: n.HostKey, acceptNew: n.HostKey == "", trust: s.trustNewHostKey,
		save:     func(fp string) error { n.HostKey = fp; return nil },
		onChange: func(from, to string) { changed = &keyChange{from, to} }}
	client, disc, err := s.d.Build(ctx, *n, password, &HostKeys{k: keys}, func(fp string) { n.TLSPin = fp }, s.d.Log)
	if err != nil {
		return nil, err
	}
	if disc != nil && disc.Role != "" && disc.Role != "master" {
		return nil, ErrNotMain
	}
	defer func() { _ = client.Logout(context.Background()) }()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := client.Login(cctx); err != nil {
		return nil, explain(err)
	}
	if _, err := client.Devices(cctx); err != nil {
		return nil, explain(err)
	}
	keys.mu.Lock() // onChange ran on the dial goroutine
	defer keys.mu.Unlock()
	return changed, nil
}

func (s *Site) trustNewHostKey() bool {
	return s.d.TrustNewHostKey != nil && s.d.TrustNewHostKey()
}

// recordKeyChange stores, publishes and notifies a deco.host_key_changed
// event for a host key that was trusted automatically.
func (s *Site) recordKeyChange(n store.Network, from, to string) {
	payload, _ := json.Marshal(map[string]string{"from": from, "to": to})
	se, err := s.d.Store.AddEvent(context.Background(), n.ID, store.Event{Type: events.DecoKeyChanged, Subject: n.Host, Payload: payload})
	if err != nil {
		s.d.Log.Error("storing event", "type", events.DecoKeyChanged, "err", err)
		return
	}
	s.d.Hub.Publish(poller.Message{Kind: "event", Data: se})
	if s.d.Notify != nil {
		s.d.Notify.Notify(se, n.Host, false)
	}
}

// explain turns a transport error into one the user can act on. Errors
// that already say what to do pass through.
func explain(err error) error {
	switch {
	case errors.Is(err, deco.ErrWrongPassword), errors.Is(err, deco.ErrAuthFailed):
		return userError{"The Deco rejected the TP-Link ID email or password.", deco.ErrWrongPassword}
	case errors.Is(err, deco.ErrOtherAdmin):
		return userError{"Another admin is signed in to the Deco. Sign out of the Deco app on other devices and try again.", deco.ErrOtherAdmin}
	case errors.Is(err, deco.ErrCertChanged):
		return userError{"The Deco presented a different security key than the one trusted for this address.", deco.ErrCertChanged}
	case errors.Is(err, ErrTLSOnly), errors.Is(err, ErrNotMain):
		return err
	}
	return userError{"Could not reach the Deco at that address: " + err.Error(), ErrUnreachable}
}

// userError reads as msg and matches kind with errors.Is.
type userError struct {
	msg  string
	kind error
}

func (e userError) Error() string { return e.msg }
func (e userError) Unwrap() error { return e.kind }

// Remove stops the connection and deletes the site with its history.
func (s *Site) Remove(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	n, ok, err := s.d.Store.GetNetwork(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoDeco
	}
	s.stop(ctx)
	return s.d.Store.DeleteNetwork(ctx, n.ID)
}

// AcceptHostKey forgets the pinned SSH host key or TLS certificate and
// reconnects, trusting the one the Deco presents next.
func (s *Site) AcceptHostKey(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	r := s.Current()
	if r == nil {
		return ErrNoDeco
	}
	n := r.Network
	if n.Transport == "luci" {
		n.TLSPin = ""
		if err := s.d.Store.SetTLSPin(ctx, n.ID, ""); err != nil {
			return err
		}
		password, err := s.d.Box.Open(n.PasswordEnc)
		if err != nil {
			return err
		}
		s.stop(ctx)
		if err := s.start(ctx, n, password, s.newHostKeys(n)); err != nil {
			s.d.Hub.Publish(poller.Message{Kind: "deco", Data: nil}) // nothing runs now
			return err
		}
		return nil
	}
	r.keys.accept()
	if err := s.d.Store.SetHostKey(ctx, n.ID, ""); err != nil {
		return err
	}
	r.Session.ClearCert() // never clears auth_failed
	r.Poller.Refresh()
	return nil
}

func (c Conn) validate() error {
	var bad []string
	if c.Host == "" || strings.ContainsAny(c.Host, " /?#@") {
		bad = append(bad, "the Deco's IP address is required")
	}
	if c.Port < 0 || c.Port > 65535 {
		bad = append(bad, "the port is out of range")
	}
	switch c.Transport {
	case "ssh":
		if c.Email == "" || !strings.Contains(c.Email, "@") {
			bad = append(bad, "the TP-Link ID email is required")
		}
	case "luci":
	default:
		bad = append(bad, "the connection must be ssh or luci")
	}
	if len(c.Password) > 256 {
		bad = append(bad, "the password is too long")
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(bad, "; "))
	}
	return nil
}

// hostKeys keeps the SSH host-key pin and lets a new key be accepted: after
// accept, the next connection trusts and stores whatever key is presented.
// When trust says so, a changed key is trusted and stored at once and
// onChange is told.
type hostKeys struct {
	mu        sync.Mutex
	pin       string
	acceptNew bool
	save      func(string) error
	trust     func() bool
	onChange  func(from, to string)
}

func (s *Site) newHostKeys(n store.Network) *hostKeys {
	// The first connection trusts and stores the key the Deco presents
	// (trust on first use, CLAUDE.md §5.1).
	return &hostKeys{pin: n.HostKey, acceptNew: n.HostKey == "", trust: s.trustNewHostKey,
		save: func(fp string) error {
			err := s.d.Store.SetHostKey(context.Background(), n.ID, fp)
			if err != nil {
				s.d.Log.Error("saving the SSH host key", "err", err)
			}
			return err
		},
		onChange: func(from, to string) { s.recordKeyChange(n, from, to) },
	}
}

func (h *hostKeys) current() (string, bool) {
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

// changed is asked when the Deco presents a key other than the pin. It
// reports whether the new key is trusted.
func (h *hostKeys) changed(from, to string) bool {
	if h.trust == nil || !h.trust() {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pin, h.acceptNew = to, false
	_ = h.save(to)
	if h.onChange != nil {
		h.onChange(from, to)
	}
	return true
}

func (h *hostKeys) accept() {
	h.mu.Lock()
	h.pin, h.acceptNew = "", true
	h.mu.Unlock()
}

// HostKeys is the pin a Builder checks the SSH host key against.
type HostKeys struct{ k *hostKeys }

// Current returns the pinned fingerprint and whether a new key is trusted.
func (h *HostKeys) Current() (pin string, acceptNew bool) { return h.k.current() }

// Seen records the fingerprint the Deco presented.
func (h *HostKeys) Seen(fp string) { h.k.seen(fp) }

// Changed is asked when the Deco presents a key other than the pin; it
// reports whether the new key is trusted (and then stores it).
func (h *HostKeys) Changed(from, to string) bool { return h.k.changed(from, to) }

// Build makes the real client: SSH (TDP discovery, TP-Link ID login,
// host-key pinning) or the local web API.
func Build(ctx context.Context, n store.Network, password string, keys *HostKeys, saveTLSPin func(string), log *slog.Logger) (deco.Client, *Discovery, error) {
	if n.Transport == "luci" {
		base := n.Host
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		c, err := luci.New(base, password, luci.Options{Pin: n.TLSPin, OnFingerprint: func(fp string) {
			log.Warn("trusting the Deco's TLS certificate on first use", "fingerprint", fp)
			saveTLSPin(fp)
		}})
		return c, nil, err
	}

	port, digest, tps := n.Port, false, false
	var disc *Discovery
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	info, err := tdp.Discover(dctx, n.Host)
	cancel()
	if err != nil {
		log.Warn("TDP discovery failed; using defaults", "err", err)
	} else {
		if !info.SSHAccess() {
			return nil, nil, fmt.Errorf("%w (model %s)", ErrTLSOnly, info.Model)
		}
		digest, tps = info.NeedAccountDigest, info.TPSBanner()
		if port == 0 {
			port = info.SSHPort()
		}
		disc = &Discovery{Model: info.Model, Role: info.Role, Port: info.SSHPort()}
		log.Info("deco discovered", "model", info.Model, "role", info.Role, "ssh_port", info.SSHPort())
	}
	if port == 0 {
		port = 20001
	}
	user := sshconn.Username(n.Email, digest, false)
	dial := func(ctx context.Context, _ func(string, ...any)) (io.ReadWriteCloser, error) {
		pin, acceptNew := keys.Current()
		return sshconn.Dial(ctx, sshconn.Options{
			Host: n.Host, Port: port, User: user, Password: password, TPSBanner: tps,
			HostKeyPin: pin, AcceptNew: acceptNew, OnHostKey: func(fp string) {
				log.Info("ssh host key", "fingerprint", fp, "trusted", acceptNew)
				keys.Seen(fp)
			},
			OnHostKeyChanged: func(from, to string) bool {
				if keys.Changed(from, to) {
					log.Warn("the Deco's SSH host key changed; trusting the new key (Settings → Advanced)", "from", from, "to", to)
					return true
				}
				log.Warn("the Deco's SSH host key changed; waiting for it to be accepted", "from", from, "to", to)
				return false
			},
		})
	}
	return tmpclient.New(dial, tmp.Options{Username: n.Email, Owner: true, Heartbeat: 3 * time.Second}), disc, nil
}
