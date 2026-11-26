// Package metrics exposes the site as Prometheus metrics (CLAUDE.md §6,
// Phase 3). Per-client series are limited to watched clients by default to
// keep label cardinality in check.
package metrics

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/t0mer/atlantis/internal/deco"
)

// Metrics holds the collectors on a private registry.
type Metrics struct {
	reg       *prometheus.Registry
	perClient atomic.Value // string: all, watched, none

	nodeUp, clients, clientUp, clientRx, clientTx *prometheus.GaugeVec
	cpu, mem, wanUp                               prometheus.Gauge
	pollDuration                                  *prometheus.HistogramVec
	pollErrors, logins                            *prometheus.CounterVec
}

// SetPerClient changes which clients get their own series (all, watched or
// none); it applies from the next poll.
func (m *Metrics) SetPerClient(perClient string) { m.perClient.Store(perClient) }

// New registers the collectors. perClient is all, watched or none.
func New(perClient string) *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	m.SetPerClient(perClient)
	g := func(name, help string, labels ...string) *prometheus.GaugeVec {
		v := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
		m.reg.MustRegister(v)
		return v
	}
	m.nodeUp = g("deco_node_up", "1 when the Deco node is connected to the mesh.", "mac", "name", "model")
	m.clients = g("deco_clients", "Online clients per node, band and network.", "node", "band", "interface")
	m.clientUp = g("deco_client_up", "1 when the client is online.", "mac", "name")
	m.clientRx = g("deco_client_rx_bytes_rate", "Client download rate in bytes per second.", "mac", "name")
	m.clientTx = g("deco_client_tx_bytes_rate", "Client upload rate in bytes per second.", "mac", "name")
	gauge := func(name, help string) prometheus.Gauge {
		v := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
		m.reg.MustRegister(v)
		return v
	}
	m.cpu = gauge("deco_cpu_usage", "Main Deco CPU load, 0..1 (absent when the model does not report it).")
	m.mem = gauge("deco_mem_usage", "Main Deco memory use, 0..1 (absent when the model does not report it).")
	m.wanUp = gauge("deco_wan_up", "1 when the main Deco reports working internet.")
	m.pollDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "deco_poll_duration_seconds", Help: "Duration of a poll loop run.", Buckets: prometheus.DefBuckets,
	}, []string{"loop"})
	m.pollErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "deco_poll_errors_total", Help: "Failed poll loop runs."}, []string{"loop", "reason"})
	m.logins = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "deco_session_logins_total", Help: "Session (re)connections."}, []string{"result"})
	m.reg.MustRegister(m.pollDuration, m.pollErrors, m.logins, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves the metrics.
func (m *Metrics) Handler() http.Handler { return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}) }

// Nodes records the node list; nodes that disappear drop their series.
func (m *Metrics) Nodes(nodes []deco.Device) {
	m.nodeUp.Reset()
	for _, n := range nodes {
		m.nodeUp.WithLabelValues(n.MAC, n.Nickname, n.Model).Set(b(n.Online))
		if n.Master {
			m.wanUp.Set(b(n.InternetOnline))
		}
	}
}

// Client is a client with the user's metadata for labelling.
type Client struct {
	deco.WifiClient
	DisplayName string
	Watch       bool
}

// Clients records per-node counts and, per the per_client setting,
// per-client presence and rates.
func (m *Metrics) Clients(list []Client) {
	m.clients.Reset()
	m.clientUp.Reset()
	m.clientRx.Reset()
	m.clientTx.Reset()
	pc, _ := m.perClient.Load().(string)
	for _, c := range list {
		if c.Online {
			band := c.Band
			if c.Wired {
				band = "wired"
			}
			m.clients.WithLabelValues(c.NodeID, band, c.Network).Inc()
		}
		if pc == "none" || (pc == "watched" && !c.Watch) {
			continue
		}
		m.clientUp.WithLabelValues(c.MAC, c.DisplayName).Set(b(c.Online))
		m.clientRx.WithLabelValues(c.MAC, c.DisplayName).Set(float64(c.DownBps) / 8)
		m.clientTx.WithLabelValues(c.MAC, c.DisplayName).Set(float64(c.UpBps) / 8)
	}
}

// Performance records the main Deco's load.
func (m *Metrics) Performance(p *deco.Performance) {
	if p == nil {
		return
	}
	m.cpu.Set(p.CPUUsage)
	m.mem.Set(p.MemUsage)
}

// Poll records one loop run; reason is "" on success.
func (m *Metrics) Poll(loop string, d time.Duration, reason string) {
	m.pollDuration.WithLabelValues(loop).Observe(d.Seconds())
	if reason != "" {
		m.pollErrors.WithLabelValues(loop, reason).Inc()
	}
}

// Login records a session (re)connection result.
func (m *Metrics) Login(result string) { m.logins.WithLabelValues(result).Inc() }

func b(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
