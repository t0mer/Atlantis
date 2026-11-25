// Package poller reads the site on two loops (CLAUDE.md §5.2): a fast one
// for clients and a slow one for nodes, Wi-Fi, firmware, WAN and load. Each
// result is stored, diffed into events, published to live subscribers and
// handed to notifications and metrics.
package poller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/events"
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/session"
	"github.com/t0mer/atlantis/internal/store"
)

// Notifier receives events (internal/notify.Dispatcher).
type Notifier interface {
	Notify(e store.Event, name string, watched bool)
}

// Options configure a Poller.
type Options struct {
	NetworkID       int64
	Fast, Slow      time.Duration
	OfflineAfter    int
	RawRetention    time.Duration
	RollupRetention time.Duration
}

// Snapshot is the latest view of the site.
type Snapshot struct {
	Nodes       []deco.Device        `json:"nodes"`
	Clients     []deco.WifiClient    `json:"clients"`
	Wireless    *deco.WirelessConfig `json:"wireless,omitempty"`
	Firmware    []deco.Firmware      `json:"firmware,omitempty"`
	WAN         *deco.WANStatus      `json:"wan,omitempty"`
	Performance *deco.Performance    `json:"performance,omitempty"`
	// WANSupported / PerformanceSupported are false when the Deco does not
	// offer them (for example in access-point mode).
	WANSupported         bool      `json:"wan_supported"`
	PerformanceSupported bool      `json:"performance_supported"`
	FastAt               time.Time `json:"fast_at,omitzero"`
	SlowAt               time.Time `json:"slow_at,omitzero"`
}

// Poller runs the loops.
type Poller struct {
	opts    Options
	sess    *session.Manager
	store   *store.Store
	notify  Notifier
	metrics *metrics.Metrics
	hub     *Hub
	log     *slog.Logger
	now     func() time.Time

	engine  *events.Engine
	refresh chan struct{}

	mu   sync.RWMutex
	snap Snapshot
}

// New returns a poller. notify and m may be nil.
func New(ctx context.Context, opts Options, sess *session.Manager, st *store.Store, notify Notifier, m *metrics.Metrics, hub *Hub, log *slog.Logger) (*Poller, error) {
	known, err := st.KnownClients(ctx, opts.NetworkID)
	if err != nil {
		return nil, err
	}
	return &Poller{
		opts: opts, sess: sess, store: st, notify: notify, metrics: m, hub: hub, log: log, now: time.Now,
		engine: events.New(known, opts.OfflineAfter), refresh: make(chan struct{}, 1),
	}, nil
}

// Snapshot returns a copy of the latest view.
func (p *Poller) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}

// Refresh asks for an immediate full poll (after a write).
func (p *Poller) Refresh() {
	select {
	case p.refresh <- struct{}{}:
	default:
	}
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d/10)+1)) // up to +10%
}

// Run polls until ctx is done. The slow loop runs first so nodes are known
// before clients are attributed to them.
func (p *Poller) Run(ctx context.Context) {
	p.Slow(ctx)
	p.Fast(ctx)
	fast := time.NewTimer(jitter(p.opts.Fast))
	slow := time.NewTimer(jitter(p.opts.Slow))
	compact := time.NewTicker(time.Hour)
	defer fast.Stop()
	defer slow.Stop()
	defer compact.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			p.Fast(ctx)
			fast.Reset(jitter(p.opts.Fast))
		case <-slow.C:
			p.Slow(ctx)
			slow.Reset(jitter(p.opts.Slow))
		case <-p.refresh:
			p.Slow(ctx)
			p.Fast(ctx)
		case <-compact.C:
			if err := p.store.Compact(ctx, p.opts.RawRetention, p.opts.RollupRetention); err != nil {
				p.log.Error("compacting history", "err", err)
			}
		}
	}
}

// sessionFatal reports errors that mean the session itself is unusable.
func sessionFatal(err error) bool {
	return errors.Is(err, deco.ErrWrongPassword) || errors.Is(err, deco.ErrAuthFailed) || errors.Is(err, deco.ErrOtherAdmin) ||
		errors.Is(err, deco.ErrCertChanged) || errors.Is(err, deco.ErrCertUnknown) || errors.Is(err, context.Canceled)
}

// reason classifies a loop error for the metrics label.
func reason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, session.ErrPaused):
		return "paused"
	case errors.Is(err, session.ErrBlocked), errors.Is(err, deco.ErrOtherAdmin):
		return "blocked"
	case errors.Is(err, session.ErrStopped), errors.Is(err, deco.ErrWrongPassword), errors.Is(err, deco.ErrCertChanged), errors.Is(err, deco.ErrCertUnknown):
		return "stopped"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "device"
}

func (p *Poller) finish(loop string, start time.Time, err error) {
	if p.metrics != nil {
		p.metrics.Poll(loop, p.now().Sub(start), reason(err))
	}
	if err != nil && !errors.Is(err, session.ErrPaused) && !errors.Is(err, context.Canceled) {
		p.log.Warn("poll failed", "loop", loop, "err", err)
	}
}

// Fast reads clients, updates presence and emits client events.
func (p *Poller) Fast(ctx context.Context) {
	start := p.now()
	var list []deco.WifiClient
	err := p.sess.Do(ctx, func(ctx context.Context, c deco.Client) error {
		var err error
		list, err = c.Clients(ctx, "")
		return err
	})
	defer func() { p.finish("fast", start, err) }()
	if err != nil {
		return
	}
	evs, presence := p.engine.Clients(list)
	if err = p.store.SaveClients(ctx, p.opts.NetworkID, list, presence); err != nil {
		return
	}
	samples := make([]store.ClientSample, 0, len(list))
	for _, c := range list {
		samples = append(samples, store.ClientSample{MAC: c.MAC, NodeID: c.NodeID, Band: c.Band, Network: c.Network,
			IP: c.IP, Online: c.Online, UpBps: c.UpBps, DownBps: c.DownBps})
	}
	if err = p.store.AddSamples(ctx, p.opts.NetworkID, nil, samples); err != nil {
		return
	}
	p.mu.Lock()
	p.snap.Clients, p.snap.FastAt = list, p.now()
	p.mu.Unlock()
	p.emit(ctx, evs)
	if p.metrics != nil {
		p.metrics.Clients(p.metricClients(ctx, list))
	}
	p.hub.Publish(Message{Kind: "clients", Data: list})
}

// metricClients attaches the user's alias and watch flag for labelling.
func (p *Poller) metricClients(ctx context.Context, list []deco.WifiClient) []metrics.Client {
	stored, _ := p.store.Clients(ctx, p.opts.NetworkID)
	meta := make(map[string]store.Client, len(stored))
	for _, c := range stored {
		meta[c.MAC] = c
	}
	out := make([]metrics.Client, 0, len(list))
	for _, c := range list {
		m := meta[c.MAC]
		name := m.DisplayName()
		if name == "" {
			name = c.Name
		}
		out = append(out, metrics.Client{WifiClient: c, DisplayName: name, Watch: m.Watch})
	}
	return out
}

// Slow reads nodes, Wi-Fi, firmware, WAN and load.
func (p *Poller) Slow(ctx context.Context) {
	start := p.now()
	var s Snapshot
	prev := p.Snapshot()
	err := p.sess.Do(ctx, func(ctx context.Context, c deco.Client) error {
		// Only the node list is required. The other reads are optional: a
		// Deco that refuses one (or a read that fails) keeps the previous
		// value instead of blanking the whole poll. Session-level failures
		// still end the poll, so the session manager sees them.
		var err error
		if s.Nodes, err = c.Devices(ctx); err != nil {
			return err
		}
		optional := func(name string, err error) error {
			if err == nil || errors.Is(err, deco.ErrUnsupported) {
				return nil
			}
			if sessionFatal(err) {
				return err
			}
			p.log.Warn("optional read failed", "read", name, "err", err)
			return nil
		}
		w, err := c.Wireless(ctx)
		if s.Wireless = w; err != nil {
			s.Wireless = prev.Wireless
		}
		if err := optional("wireless", err); err != nil {
			return err
		}
		fw, err := c.Firmware(ctx)
		if s.Firmware = fw; err != nil {
			s.Firmware = prev.Firmware
		}
		if err := optional("firmware", err); err != nil {
			return err
		}
		s.WAN, err = c.WAN(ctx)
		s.WANSupported = err == nil
		if err := optional("wan", err); err != nil {
			return err
		}
		s.Performance, err = c.Performance(ctx)
		s.PerformanceSupported = err == nil
		return optional("performance", err)
	})
	defer func() { p.finish("slow", start, err) }()
	if err != nil {
		return
	}
	evs := p.engine.Nodes(s.Nodes)
	evs = append(evs, p.engine.WAN(s.WAN)...)
	if err = p.store.SaveNodes(ctx, p.opts.NetworkID, s.Nodes); err != nil {
		return
	}
	p.mu.Lock()
	clients := p.snap.Clients
	p.mu.Unlock()
	perNode := map[string]int{}
	for _, c := range clients {
		if c.Online {
			perNode[c.NodeID]++
		}
	}
	samples := make([]store.NodeSample, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		ns := store.NodeSample{NodeID: n.ID, Online: n.Online, InternetOnline: n.InternetOnline, Clients: perNode[n.ID]}
		for _, v := range n.Backhaul.Signal {
			ns.Signal = max(ns.Signal, v)
		}
		if n.Master && s.Performance != nil {
			cpu, mem := s.Performance.CPUUsage, s.Performance.MemUsage
			ns.CPU, ns.Mem = &cpu, &mem
		}
		samples = append(samples, ns)
	}
	if err = p.store.AddSamples(ctx, p.opts.NetworkID, samples, nil); err != nil {
		return
	}
	p.mu.Lock()
	s.Clients, s.FastAt, s.SlowAt = p.snap.Clients, p.snap.FastAt, p.now()
	p.snap = s
	p.mu.Unlock()
	p.emit(ctx, evs)
	if p.metrics != nil {
		p.metrics.Nodes(s.Nodes)
		p.metrics.Performance(s.Performance)
	}
	p.hub.Publish(Message{Kind: "snapshot", Data: p.Snapshot()})
}

// emit stores events, publishes them and hands them to notifications.
func (p *Poller) emit(ctx context.Context, evs []events.Event) {
	for _, e := range evs {
		payload, _ := json.Marshal(e.Payload)
		se, err := p.store.AddEvent(ctx, p.opts.NetworkID, store.Event{Type: e.Type, Subject: e.Subject, Payload: payload})
		if err != nil {
			p.log.Error("storing event", "type", e.Type, "err", err)
			continue
		}
		p.hub.Publish(Message{Kind: "event", Data: se})
		if p.notify != nil {
			name, watched := p.subjectName(ctx, e)
			p.notify.Notify(se, name, watched)
		}
	}
}

// subjectName resolves a display name and the watch flag for notifications.
func (p *Poller) subjectName(ctx context.Context, e events.Event) (string, bool) {
	if c, err := p.store.GetClient(ctx, p.opts.NetworkID, e.Subject); err == nil {
		return c.DisplayName(), c.Watch
	}
	if n, ok := e.Payload["name"].(string); ok && n != "" {
		return n, false
	}
	return e.Subject, false
}
