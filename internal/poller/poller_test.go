package poller

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
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/session"
	"github.com/t0mer/atlantis/internal/store"
)

// fakeDeco is a scripted site.
type fakeDeco struct {
	mu      sync.Mutex
	nodes   []deco.Device
	clients []deco.WifiClient
	wanErr  error
}

func (f *fakeDeco) Login(context.Context) error  { return nil }
func (f *fakeDeco) Logout(context.Context) error { return nil }
func (f *fakeDeco) Devices(context.Context) ([]deco.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]deco.Device(nil), f.nodes...), nil
}
func (f *fakeDeco) Clients(context.Context, string) ([]deco.WifiClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]deco.WifiClient(nil), f.clients...), nil
}
func (f *fakeDeco) WAN(context.Context) (*deco.WANStatus, error) { return nil, f.wanErr }
func (f *fakeDeco) Performance(context.Context) (*deco.Performance, error) {
	return &deco.Performance{CPUUsage: 0.2, MemUsage: 0.5}, nil
}
func (f *fakeDeco) Wireless(context.Context) (*deco.WirelessConfig, error) {
	return &deco.WirelessConfig{Bands: map[string]deco.Band{"band2_4": {}}}, nil
}
func (f *fakeDeco) Firmware(context.Context) ([]deco.Firmware, error) {
	return nil, deco.ErrUnsupported
}
func (f *fakeDeco) Raw(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

type notified struct {
	mu  sync.Mutex
	got []string
}

func (n *notified) Notify(e store.Event, name string, watched bool) {
	n.mu.Lock()
	n.got = append(n.got, e.Type+":"+name)
	n.mu.Unlock()
}

func setup(t *testing.T, f *fakeDeco) (*Poller, *store.Store, *notified, *Hub, *session.Manager) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n, _ := st.EnsureNetwork(ctx, store.Network{Host: "h", Transport: "ssh"})
	sess := session.New(f, nil)
	hub := NewHub()
	no := &notified{}
	p, err := New(ctx, Options{NetworkID: n.ID, Fast: time.Minute, Slow: time.Minute, OfflineAfter: 2, RawRetention: time.Hour, RollupRetention: 2 * time.Hour},
		sess, st, no, metrics.New("all"), hub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return p, st, no, hub, sess
}

func TestPollCycle(t *testing.T) {
	f := &fakeDeco{
		nodes:   []deco.Device{{ID: "m", Master: true, Online: true, InternetOnline: true, Nickname: "Main"}, {ID: "s", Online: true, Nickname: "Office"}},
		clients: []deco.WifiClient{{MAC: "c1", Name: "Phone", Online: true, NodeID: "s"}},
		wanErr:  deco.ErrUnsupported,
	}
	p, st, no, hub, _ := setup(t, f)
	ctx := context.Background()
	msgs, cancel := hub.Subscribe()
	defer cancel()

	p.Slow(ctx)
	p.Fast(ctx)
	snap := p.Snapshot()
	if len(snap.Nodes) != 2 || len(snap.Clients) != 1 || snap.WANSupported || !snap.PerformanceSupported || snap.Performance.CPUUsage != 0.2 {
		t.Fatalf("%+v", snap)
	}
	if nodes, _ := st.Nodes(ctx, 1); len(nodes) != 2 {
		t.Fatal("nodes not stored")
	}

	f.mu.Lock()
	f.clients = append(f.clients, deco.WifiClient{MAC: "c2", Name: "Laptop", Online: true, NodeID: "m"})
	f.nodes = f.nodes[:1] // the satellite drops out
	f.mu.Unlock()
	p.Fast(ctx)
	p.Slow(ctx)
	evs, _ := st.Events(ctx, 1, store.EventFilter{})
	types := map[string]bool{}
	for _, e := range evs {
		types[e.Type+":"+e.Subject] = true
	}
	if !types["client.joined:c2"] || !types["node.offline:s"] {
		t.Fatalf("events: %v", types)
	}
	no.mu.Lock()
	if len(no.got) != 2 {
		t.Fatalf("notifications: %v", no.got)
	}
	no.mu.Unlock()
	var kinds []string
	for len(msgs) > 0 {
		kinds = append(kinds, (<-msgs).Kind)
	}
	if len(kinds) < 4 {
		t.Fatalf("live updates: %v", kinds)
	}
	hist, _ := st.NodeHistory(ctx, 1, "m", time.Time{})
	if len(hist) != 2 || hist[0].CPU == nil {
		t.Fatalf("node samples: %+v", hist)
	}
}

func TestPausedSessionSkipsPolls(t *testing.T) {
	f := &fakeDeco{nodes: []deco.Device{{ID: "m", Master: true, Online: true}}}
	p, st, _, _, sess := setup(t, f)
	ctx := context.Background()
	sess.Pause(ctx)
	p.Slow(ctx)
	p.Fast(ctx)
	if nodes, _ := st.Nodes(ctx, 1); len(nodes) != 0 {
		t.Fatal("polled while paused")
	}
}

func TestRunStopsWithContext(t *testing.T) {
	f := &fakeDeco{nodes: []deco.Device{{ID: "m", Master: true, Online: true}}}
	p, _, _, _, _ := setup(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	p.Refresh()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

type flakyWireless struct{ *fakeDeco }

func (flakyWireless) Wireless(context.Context) (*deco.WirelessConfig, error) {
	return nil, errors.New("tmp: WIRELESS_GET returned error_code -40101")
}

// A failing optional read must not blank the poll.
func TestOptionalReadFailureKeepsNodes(t *testing.T) {
	f := &fakeDeco{nodes: []deco.Device{{ID: "m", Master: true, Online: true}}}
	p, st, _, _, sess := setup(t, f)
	p.sess = session.New(flakyWireless{f}, nil)
	_ = sess
	p.Slow(context.Background())
	if nodes, _ := st.Nodes(context.Background(), 1); len(nodes) != 1 {
		t.Fatal("nodes dropped because Wi-Fi failed")
	}
	if p.Snapshot().Wireless != nil {
		t.Fatal("no previous Wi-Fi to keep")
	}
}

// A client that drops out of the device's list is recorded offline once
// the engine marks it so, rather than leaving a gap in its history.
func TestAbsentClientRecordedOffline(t *testing.T) {
	f := &fakeDeco{clients: []deco.WifiClient{{MAC: "c1", Online: true}}}
	p, st, _, _, _ := setup(t, f)
	ctx := context.Background()
	p.Fast(ctx)
	f.mu.Lock()
	f.clients = nil
	f.mu.Unlock()
	p.Fast(ctx) // miss 1: still online
	p.Fast(ctx) // miss 2: offline
	hist, err := st.ClientHistory(ctx, 1, "c1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 || hist[len(hist)-1].Online != 0 {
		t.Fatalf("want a trailing offline sample: %+v", hist)
	}
}
