package site

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

type fakeClient struct {
	mu       sync.Mutex
	loginErr error
	logins   int
	logouts  int
}

func (f *fakeClient) Login(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins++
	return f.loginErr
}
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts++
	return nil
}
func (f *fakeClient) Devices(context.Context) ([]deco.Device, error) {
	if err := f.Login(context.Background()); err != nil {
		return nil, err
	}
	return []deco.Device{{ID: "m", Master: true, Online: true}}, nil
}
func (f *fakeClient) Clients(context.Context, string) ([]deco.WifiClient, error) { return nil, nil }
func (f *fakeClient) WAN(context.Context) (*deco.WANStatus, error)               { return nil, deco.ErrUnsupported }
func (f *fakeClient) Performance(context.Context) (*deco.Performance, error) {
	return nil, deco.ErrUnsupported
}
func (f *fakeClient) Wireless(context.Context) (*deco.WirelessConfig, error) {
	return nil, deco.ErrUnsupported
}
func (f *fakeClient) Firmware(context.Context) ([]deco.Firmware, error) { return nil, nil }
func (f *fakeClient) Raw(context.Context, string, string) (json.RawMessage, error) {
	return nil, deco.ErrUnsupported
}

type noNotify struct{}

func (noNotify) Notify(store.Event, string, bool) {}

type harness struct {
	s        *Site
	st       *store.Store
	box      *secret.Box
	client   *fakeClient
	disc     *Discovery
	build    error
	failHost string // Build fails with ErrTLSOnly for this host
	builds   int
	mu       sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(secret.NewKey())
	h := &harness{st: st, box: box, client: &fakeClient{}, disc: &Discovery{Model: "M5", Role: "master", Port: 20001}}
	h.s = New(ctx, Deps{Poll: Poll{Fast: time.Hour, Slow: time.Hour, OfflineAfterMisses: 2}, Store: st, Box: box, Hub: poller.NewHub(), Notify: noNotify{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Build: func(_ context.Context, n store.Network, _ string, _ *HostKeys, _ func(string), _ *slog.Logger) (deco.Client, *Discovery, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.builds++
			if n.Host == h.failHost {
				return nil, nil, ErrTLSOnly
			}
			if h.build != nil {
				return nil, nil, h.build
			}
			return h.client, h.disc, nil
		}})
	t.Cleanup(func() { h.s.Close(context.Background()); cancel(); st.Close() })
	return h
}

var conn = Conn{Host: "192.0.2.1", Email: "owner@example.com", Password: "decopass"}

func TestStartsEmpty(t *testing.T) {
	h := newHarness(t)
	if err := h.s.Start(context.Background()); err != nil || h.s.Current() != nil {
		t.Fatalf("with no stored Deco nothing runs: %v", err)
	}
}

func TestConnectStoresAndRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	n, err := h.s.Connect(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	run := h.s.Current()
	if run == nil || run.Network.ID != n.ID || run.Discovery.Model != "M5" {
		t.Fatalf("%+v", run)
	}
	got, ok, _ := h.st.GetNetwork(ctx)
	if !ok || got.Host != "192.0.2.1" || got.Transport != "ssh" {
		t.Fatalf("%+v", got)
	}
	if p, _ := h.box.Open(got.PasswordEnc); p != "decopass" {
		t.Fatal("the password must be stored sealed")
	}

	// The stored password is never sent to a different address, port or
	// account: it would hand the TP-Link ID password to that machine.
	h.st.SaveNodes(ctx, n.ID, []deco.Device{{ID: "m", Master: true, Online: true}})
	builds := h.builds
	for _, c := range []Conn{
		{Host: "192.0.2.66", Email: "owner@example.com"},
		{Host: "192.0.2.1", Port: 2222, Email: "owner@example.com"},
		{Host: "192.0.2.1", Email: "other@example.com"},
		{Host: "192.0.2.1", Email: "owner@example.com", Transport: "luci"},
	} {
		if _, err := h.s.Connect(ctx, c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v without a password: %v", c, err)
		}
	}
	if h.builds != builds {
		t.Fatal("the stored password was sent to a new target")
	}
	// The same target without a password keeps it; a new one with a password works.
	if _, err := h.s.Connect(ctx, Conn{Host: "192.0.2.1", Email: "Owner@Example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Connect(ctx, Conn{Host: "192.0.2.2", Email: "owner@example.com", Password: "decopass2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = h.st.GetNetwork(ctx)
	if p, _ := h.box.Open(got.PasswordEnc); p != "decopass2" || got.Host != "192.0.2.2" || got.ID != n.ID {
		t.Fatalf("%+v", got)
	}
	if nodes, _ := h.st.Nodes(ctx, n.ID); len(nodes) != 1 {
		t.Fatal("changing the connection must keep history")
	}
}

func TestWrongPasswordIsTriedOnceAndNotStored(t *testing.T) {
	h := newHarness(t)
	h.client.loginErr = deco.ErrWrongPassword
	_, err := h.s.Connect(context.Background(), conn)
	if !errors.Is(err, deco.ErrWrongPassword) {
		t.Fatalf("got %v", err)
	}
	if h.client.logins != 1 {
		t.Fatalf("a rejected password must be sent once, not %d times (lockout)", h.client.logins)
	}
	if _, ok, _ := h.st.GetNetwork(context.Background()); ok || h.s.Current() != nil {
		t.Fatal("a failed check must store and run nothing")
	}
}

func TestRejectsTLSOnlyAndSatellites(t *testing.T) {
	h := newHarness(t)
	h.build = ErrTLSOnly
	if _, err := h.s.Connect(context.Background(), conn); !errors.Is(err, ErrTLSOnly) {
		t.Fatalf("got %v", err)
	}
	h.build = nil
	h.disc = &Discovery{Model: "M5", Role: "slave"}
	if _, err := h.s.Connect(context.Background(), conn); !errors.Is(err, ErrNotMain) {
		t.Fatalf("got %v", err)
	}
}

func TestFailedChangeRestartsThePreviousConnection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.s.Connect(ctx, conn); err != nil {
		t.Fatal(err)
	}
	before := h.s.Current()
	h.failHost = "192.0.2.9"
	if _, err := h.s.Connect(ctx, Conn{Host: "192.0.2.9", Email: "owner@example.com", Password: "other"}); !errors.Is(err, ErrTLSOnly) {
		t.Fatalf("got %v", err)
	}
	after := h.s.Current()
	if after == nil || after == before || after.Network.Host != "192.0.2.1" {
		t.Fatalf("the previous connection must be running again: %+v", after)
	}
	if got, _, _ := h.st.GetNetwork(ctx); got.Host != "192.0.2.1" {
		t.Fatalf("a failed change must not be stored: %+v", got)
	}
}

func TestRemoveStopsAndDeletes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.s.Connect(ctx, conn); err != nil {
		t.Fatal(err)
	}
	run := h.s.Current()
	if err := h.s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.done:
	default:
		t.Fatal("the poller must have stopped")
	}
	if h.s.Current() != nil {
		t.Fatal("nothing must run after remove")
	}
	if _, ok, _ := h.st.GetNetwork(ctx); ok {
		t.Fatal("the site must be deleted")
	}
	if err := h.s.Remove(ctx); !errors.Is(err, ErrNoDeco) {
		t.Fatalf("got %v", err)
	}
}

func TestConnValidation(t *testing.T) {
	h := newHarness(t)
	for _, c := range []Conn{
		{Email: "a@example.com", Password: "x"},
		{Host: "192.0.2.1", Password: "x"},
		{Host: "192.0.2.1/x", Email: "a@example.com", Password: "x"},
		{Host: "192.0.2.1", Email: "a@example.com", Password: "x", Port: 70000},
		{Host: "192.0.2.1", Email: "a@example.com", Password: "x", Transport: "telnet"},
		{Host: "192.0.2.1", Email: "a@example.com"}, // no stored password to keep
	} {
		if _, err := h.s.Connect(context.Background(), c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: got %v", c, err)
		}
	}
	if h.builds != 0 {
		t.Fatal("invalid settings must never reach the Deco")
	}
}

// A change that fails after the browser gave up (cancelled request) must
// still bring the previous connection back.
func TestFailedChangeFromACancelledRequestRestarts(t *testing.T) {
	h := newHarness(t)
	if _, err := h.s.Connect(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	h.failHost = "192.0.2.9"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.s.Connect(ctx, Conn{Host: "192.0.2.9", Email: "owner@example.com", Password: "other"}); err == nil {
		t.Fatal("expected the check to fail")
	}
	if run := h.s.Current(); run == nil || run.Network.Host != "192.0.2.1" {
		t.Fatalf("the previous connection must be running again: %+v", run)
	}
	if h.s.Changing() {
		t.Fatal("still marked as changing")
	}
}

// keyClient checks the SSH host key the way the real dial does: the pin
// first, then the site's decision on a changed key.
type keyClient struct {
	*fakeClient
	keys *HostKeys
	key  func() string // what the Deco presents
}

func (k keyClient) Login(ctx context.Context) error {
	presented := k.key()
	pin, acceptNew := k.keys.Current()
	switch {
	case pin == "":
		k.keys.Seen(presented)
		if !acceptNew {
			return deco.ErrCertUnknown
		}
	case pin != presented && !k.keys.Changed(pin, presented):
		return deco.ErrCertChanged
	}
	return k.fakeClient.Login(ctx)
}

func (k keyClient) Devices(ctx context.Context) ([]deco.Device, error) {
	if err := k.Login(ctx); err != nil { // the real client dials lazily
		return nil, err
	}
	return k.fakeClient.Devices(ctx)
}

type notified struct {
	mu  sync.Mutex
	evs []store.Event
}

func (n *notified) Notify(e store.Event, _ string, _ bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evs = append(n.evs, e)
}

func (n *notified) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.evs)
}

func TestChangedHostKey(t *testing.T) {
	for _, trust := range []bool{true, false} {
		t.Run(map[bool]string{true: "trusted", false: "manual"}[trust], func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			var mu sync.Mutex
			presented := "SHA256:old"
			key := func() string { mu.Lock(); defer mu.Unlock(); return presented }
			nt := &notified{}
			deps := h.s.d
			deps.Notify = nt
			deps.TrustNewHostKey = func() bool { return trust }
			deps.Build = func(_ context.Context, _ store.Network, _ string, keys *HostKeys, _ func(string), _ *slog.Logger) (deco.Client, *Discovery, error) {
				return keyClient{&fakeClient{}, keys, key}, h.disc, nil
			}
			h.s.d = deps
			if _, err := h.s.Connect(ctx, conn); err != nil {
				t.Fatal(err)
			}
			if n, _, _ := h.st.GetNetwork(ctx); n.HostKey != "SHA256:old" {
				t.Fatalf("first key pinned: %q", n.HostKey)
			}
			h.s.Close(ctx)

			// A firmware update changes the key; Atlantis starts again.
			mu.Lock()
			presented = "SHA256:new"
			mu.Unlock()
			s2 := New(ctx, deps)
			t.Cleanup(func() { s2.Close(context.Background()) })
			if err := s2.Start(ctx); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				st := s2.Current().Session.Status().State
				if (trust && st == "connected") || (!trust && st == "cert_changed") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			n, _, _ := h.st.GetNetwork(ctx)
			evs, _ := h.st.Events(ctx, n.ID, store.EventFilter{})
			if !trust {
				if st := s2.Current().Session.Status().State; st != "cert_changed" || n.HostKey != "SHA256:old" || len(evs) != 0 || nt.count() != 0 {
					t.Fatalf("manual: state %s, pin %q, %d events, %d notified", st, n.HostKey, len(evs), nt.count())
				}
				return
			}
			if st := s2.Current().Session.Status().State; st != "connected" || n.HostKey != "SHA256:new" {
				t.Fatalf("trusted: state %s, pin %q", st, n.HostKey)
			}
			if len(evs) != 1 || evs[0].Type != "deco.host_key_changed" || evs[0].Subject != conn.Host ||
				string(evs[0].Payload) != `{"from":"SHA256:old","to":"SHA256:new"}` || nt.count() != 1 {
				t.Fatalf("event: %+v, notified %d", evs, nt.count())
			}

			// Changing the connection settings trusts a new key the same way.
			s2.Close(ctx)
			mu.Lock()
			presented = "SHA256:newer"
			mu.Unlock()
			s3 := New(ctx, deps)
			t.Cleanup(func() { s3.Close(context.Background()) })
			c := conn
			c.Password = ""
			if _, err := s3.Connect(ctx, c); err != nil {
				t.Fatal(err)
			}
			n, _, _ = h.st.GetNetwork(ctx)
			evs, _ = h.st.Events(ctx, n.ID, store.EventFilter{})
			if n.HostKey != "SHA256:newer" || len(evs) != 2 || nt.count() != 2 {
				t.Fatalf("connect: pin %q, %d events, %d notified", n.HostKey, len(evs), nt.count())
			}
		})
	}
}
