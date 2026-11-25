// Package events turns successive polls into events (CLAUDE.md §5.3). It is
// a pure state machine: feed it polls, get events back. Persisting and
// notifying are the caller's job.
package events

import (
	"sort"

	"github.com/t0mer/atlantis/internal/deco"
)

// Event types.
const (
	ClientJoined    = "client.joined"  // a MAC seen for the first time ever
	ClientOnline    = "client.online"  // presence change, debounced
	ClientOffline   = "client.offline" // after N missed polls
	NodeOnline      = "node.online"
	NodeOffline     = "node.offline" // disconnected or missing from the list
	WANUp           = "wan.up"
	WANDown         = "wan.down"
	WANIPChanged    = "wan.ip_changed"
	FirmwareChanged = "firmware.changed"
)

// All lists every event type, for settings and channel filters.
var All = []string{ClientJoined, ClientOnline, ClientOffline, NodeOnline, NodeOffline, WANUp, WANDown, WANIPChanged, FirmwareChanged}

// Event is one change. Subject is a client MAC or node ID.
type Event struct {
	Type    string         `json:"type"`
	Subject string         `json:"subject"`
	Payload map[string]any `json:"payload"`
}

// Engine holds what the previous polls saw.
type Engine struct {
	offlineAfter int
	known        map[string]bool // every client MAC ever seen
	baseline     bool            // the store had no clients: don't announce all of them as new

	clientsSeen bool
	clientOn    map[string]bool
	misses      map[string]int

	nodesSeen bool
	nodeOn    map[string]bool
	firmware  map[string]string

	wanSeen bool
	wanUp   bool
	wanIP   string
}

// New returns an engine. known is the set of client MACs already in the
// store; offlineAfter is how many polls a client must be missing (or
// reported offline) before client.offline (minimum 1).
func New(known map[string]bool, offlineAfter int) *Engine {
	if known == nil {
		known = map[string]bool{}
	}
	e := &Engine{
		offlineAfter: max(offlineAfter, 1),
		known:        known,
		baseline:     len(known) == 0,
		clientOn:     map[string]bool{},
		misses:       map[string]int{},
		nodeOn:       map[string]bool{},
		firmware:     map[string]string{},
	}
	for mac := range known {
		e.clientOn[mac] = false // a known client seen later comes online
	}
	return e
}

// SetOfflineAfter changes how many polls a client must be missing before it
// is reported offline.
func (e *Engine) SetOfflineAfter(n int) { e.offlineAfter = max(n, 1) }

// OfflineAfter returns the current threshold.
func (e *Engine) OfflineAfter() int { return e.offlineAfter }

func clientPayload(c deco.WifiClient) map[string]any {
	return map[string]any{"name": c.Name, "ip": c.IP, "node": c.NodeID, "band": c.Band, "network": c.Network, "wired": c.Wired}
}

// Clients diffs a client poll. It returns the events and the debounced
// presence of every client the engine knows about.
func (e *Engine) Clients(list []deco.WifiClient) ([]Event, map[string]bool) {
	var out []Event
	present := map[string]deco.WifiClient{}
	for _, c := range list {
		if c.Online {
			present[c.MAC] = c
		}
	}
	first := !e.clientsSeen
	e.clientsSeen = true

	for _, c := range list {
		if !e.known[c.MAC] {
			e.known[c.MAC] = true
			if !e.baseline {
				out = append(out, Event{ClientJoined, c.MAC, clientPayload(c)})
			}
		}
	}
	e.baseline = false // only the very first poll of a fresh store is a baseline

	macs := map[string]bool{}
	for m := range e.clientOn {
		macs[m] = true
	}
	for _, c := range list {
		macs[c.MAC] = true
	}
	byMAC := map[string]deco.WifiClient{}
	for _, c := range list {
		byMAC[c.MAC] = c
	}
	for mac := range macs {
		c, isPresent := present[mac]
		was, tracked := e.clientOn[mac]
		switch {
		case isPresent:
			e.misses[mac] = 0
			// A brand-new MAC gets client.joined only; online is for returns.
			if !first && tracked && !was {
				out = append(out, Event{ClientOnline, mac, clientPayload(c)})
			}
			e.clientOn[mac] = true
		case first:
			e.clientOn[mac] = false
		default:
			e.misses[mac]++
			if was && e.misses[mac] >= e.offlineAfter {
				p := map[string]any{"missed_polls": e.misses[mac]}
				if lc, ok := byMAC[mac]; ok {
					p = clientPayload(lc)
					p["missed_polls"] = e.misses[mac]
				}
				out = append(out, Event{ClientOffline, mac, p})
				e.clientOn[mac] = false
			} else if !tracked {
				e.clientOn[mac] = false
			}
		}
	}
	presence := make(map[string]bool, len(e.clientOn))
	for m, on := range e.clientOn {
		presence[m] = on
	}
	sortEvents(out)
	return out, presence
}

func nodePayload(n deco.Device) map[string]any {
	return map[string]any{"name": n.Nickname, "model": n.Model, "master": n.Master, "ip": n.IP}
}

// Nodes diffs a node poll: online changes (a node missing from the list is
// offline), the master's internet as wan.up/wan.down, and firmware changes.
func (e *Engine) Nodes(list []deco.Device) []Event {
	var out []Event
	first := !e.nodesSeen
	e.nodesSeen = true
	seen := map[string]bool{}
	for _, n := range list {
		seen[n.ID] = true
		was, tracked := e.nodeOn[n.ID]
		if !first && (!tracked || was != n.Online) {
			t := NodeOffline
			if n.Online {
				t = NodeOnline
			}
			if tracked || n.Online {
				out = append(out, Event{t, n.ID, nodePayload(n)})
			}
		}
		e.nodeOn[n.ID] = n.Online
		if old, ok := e.firmware[n.ID]; ok && n.SoftwareVer != "" && old != n.SoftwareVer {
			p := nodePayload(n)
			p["from"], p["to"] = old, n.SoftwareVer
			out = append(out, Event{FirmwareChanged, n.ID, p})
		}
		if n.SoftwareVer != "" {
			e.firmware[n.ID] = n.SoftwareVer
		}
		if n.Master {
			out = append(out, e.internet(n.InternetOnline, map[string]any{"error": n.InternetError})...)
		}
	}
	for id, on := range e.nodeOn {
		if !seen[id] && on {
			e.nodeOn[id] = false
			out = append(out, Event{NodeOffline, id, map[string]any{"missing": true}})
		}
	}
	sortEvents(out)
	return out
}

func (e *Engine) internet(up bool, p map[string]any) []Event {
	if !e.wanSeen {
		e.wanSeen, e.wanUp = true, up
		return nil
	}
	if up == e.wanUp {
		return nil
	}
	e.wanUp = up
	if up {
		return []Event{{WANUp, "wan", p}}
	}
	return []Event{{WANDown, "wan", p}}
}

// WAN diffs the WAN address. Up/down comes from Nodes (the master's
// internet status, as the app shows it).
func (e *Engine) WAN(w *deco.WANStatus) []Event {
	if w == nil || w.IP == "" {
		return nil
	}
	old := e.wanIP
	e.wanIP = w.IP
	if old == "" || old == w.IP {
		return nil
	}
	return []Event{{WANIPChanged, "wan", map[string]any{"from": old, "to": w.IP}}}
}

func sortEvents(ev []Event) {
	sort.SliceStable(ev, func(i, j int) bool {
		if ev[i].Type != ev[j].Type {
			return ev[i].Type < ev[j].Type
		}
		return ev[i].Subject < ev[j].Subject
	})
}
