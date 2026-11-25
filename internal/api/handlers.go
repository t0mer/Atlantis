package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/events"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/session"
	"github.com/t0mer/atlantis/internal/site"
	"github.com/t0mer/atlantis/internal/store"
)

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	snap := runOf(r.Context()).Poller.Snapshot()
	st := runOf(r.Context()).Session.Status()
	out := map[string]any{"session": st, "last_fast_poll": snap.FastAt, "last_slow_poll": snap.SlowAt}
	code := http.StatusOK
	if snap.SlowAt.IsZero() || s.now().Sub(snap.SlowAt) > 3*s.Site.Poll().Slow {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, out)
}

// clientCounts counts online clients by connection.
func clientCounts(cs []deco.WifiClient) map[string]int {
	out := map[string]int{"total": 0, "wired": 0, "guest": 0, "band2_4": 0, "band5": 0, "band6": 0, "band60": 0}
	for _, c := range cs {
		if !c.Online {
			continue
		}
		out["total"]++
		switch {
		case c.Wired:
			out["wired"]++
		case c.Band == "band2_4":
			out["band2_4"]++
		case strings.HasPrefix(c.Band, "band5"):
			out["band5"]++
		case c.Band == "band60":
			out["band60"]++
		case strings.HasPrefix(c.Band, "band6"):
			out["band6"]++
		}
		if c.Network == deco.NetworkGuest {
			out["guest"]++
		}
	}
	return out
}

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	snap := runOf(r.Context()).Poller.Snapshot()
	online := 0
	var internet *bool
	for _, n := range snap.Nodes {
		if n.Online {
			online++
		}
		if n.Master {
			v := n.InternetOnline
			internet = &v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":                  runOf(r.Context()).Network.Host,
		"session":               runOf(r.Context()).Session.Status(),
		"internet_online":       internet,
		"wan":                   snap.WAN,
		"wan_supported":         snap.WANSupported,
		"performance":           snap.Performance,
		"performance_supported": snap.PerformanceSupported,
		"nodes":                 map[string]int{"online": online, "total": len(snap.Nodes)},
		"clients":               clientCounts(snap.Clients),
		"updated":               map[string]time.Time{"fast": snap.FastAt, "slow": snap.SlowAt},
	})
}

// nodeView merges the stored row (first/last seen) with the live device.
type nodeView struct {
	deco.Device
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Clients   int       `json:"clients"`
}

func (s *Server) nodeViews(ctx context.Context) ([]nodeView, error) {
	stored, err := s.store.Nodes(ctx, runOf(ctx).Network.ID)
	if err != nil {
		return nil, err
	}
	snap := runOf(ctx).Poller.Snapshot()
	live := map[string]deco.Device{}
	for _, n := range snap.Nodes {
		live[n.ID] = n
	}
	perNode := map[string]int{}
	for _, c := range snap.Clients {
		if c.Online {
			perNode[c.NodeID]++
		}
	}
	out := make([]nodeView, 0, len(stored))
	for _, n := range stored {
		d, ok := live[n.ID]
		if !ok { // gone from the last poll: show what is stored
			d = deco.Device{ID: n.ID, MAC: n.MAC, Nickname: n.Nickname, Model: n.Model, HardwareVer: n.HardwareVer,
				SoftwareVer: n.SoftwareVer, IP: n.IP, Master: n.Master, ParentID: n.ParentID}
		}
		out = append(out, nodeView{Device: d, FirstSeen: n.FirstSeen, LastSeen: n.LastSeen, Clients: perNode[n.ID]})
	}
	return out, nil
}

func (s *Server) nodes(w http.ResponseWriter, r *http.Request) {
	v, err := s.nodeViews(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// since parses ?range=24h|7d|30d (default 24h).
func (s *Server) since(r *http.Request) (time.Time, error) {
	switch rg := r.URL.Query().Get("range"); rg {
	case "", "24h":
		return s.now().Add(-24 * time.Hour), nil
	case "7d":
		return s.now().Add(-7 * 24 * time.Hour), nil
	case "30d":
		return s.now().Add(-30 * 24 * time.Hour), nil
	default:
		return time.Time{}, fmt.Errorf("range must be 24h, 7d or 30d, not %q", rg)
	}
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	since, err := s.since(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	views, err := s.nodeViews(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	i := slices.IndexFunc(views, func(v nodeView) bool { return v.ID == id })
	if i < 0 {
		writeError(w, http.StatusNotFound, "no such node")
		return
	}
	hist, err := s.store.NodeHistory(r.Context(), runOf(r.Context()).Network.ID, id, since)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": views[i], "history": hist})
}

// clientView is a client as the API returns it.
type clientView struct {
	MAC         string    `json:"mac"`
	Name        string    `json:"name"`
	Alias       string    `json:"alias"`
	DisplayName string    `json:"display_name"`
	Type        string    `json:"type"`
	Watch       bool      `json:"watch"`
	Tags        []string  `json:"tags"`
	Online      bool      `json:"online"`
	IP          string    `json:"ip"`
	NodeID      string    `json:"node_id"`
	Band        string    `json:"band"`
	Network     string    `json:"network"`
	Wired       bool      `json:"wired"`
	SignalLevel int       `json:"signal_level"`
	UpBps       int64     `json:"up_bps"`
	DownBps     int64     `json:"down_bps"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	// Private reports a randomised (locally administered) MAC.
	Private bool `json:"private_mac"`
}

func privateMAC(mac string) bool {
	if len(mac) < 2 {
		return false
	}
	b, err := strconv.ParseUint(mac[:2], 16, 8)
	return err == nil && b&0x02 != 0
}

func (s *Server) clientViews(ctx context.Context) ([]clientView, error) {
	stored, err := s.store.Clients(ctx, runOf(ctx).Network.ID)
	if err != nil {
		return nil, err
	}
	live := map[string]deco.WifiClient{}
	for _, c := range runOf(ctx).Poller.Snapshot().Clients {
		live[c.MAC] = c
	}
	out := make([]clientView, 0, len(stored))
	for _, c := range stored {
		v := clientView{MAC: c.MAC, Name: c.Name, Alias: c.Alias, DisplayName: c.DisplayName(), Type: c.Type,
			Watch: c.Watch, Tags: c.Tags, Online: c.Online, IP: c.LastIP, NodeID: c.LastNode, Band: c.LastBand,
			Network: c.LastNetwork, Wired: c.Wired, FirstSeen: c.FirstSeen, LastSeen: c.LastSeen, Private: privateMAC(c.MAC)}
		if v.Tags == nil {
			v.Tags = []string{}
		}
		if l, ok := live[c.MAC]; ok && l.Online {
			v.SignalLevel, v.UpBps, v.DownBps = l.SignalLevel, l.UpBps, l.DownBps
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) clients(w http.ResponseWriter, r *http.Request) {
	all, err := s.clientViews(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	q := r.URL.Query()
	qs := strings.ToLower(q.Get("q"))
	out := all[:0]
	for _, c := range all {
		switch {
		case q.Get("online") == "true" && !c.Online, q.Get("online") == "false" && c.Online:
		case q.Get("node") != "" && c.NodeID != q.Get("node"):
		case q.Get("band") == "wired" && !c.Wired:
		case q.Get("band") != "" && q.Get("band") != "wired" && !strings.HasPrefix(c.Band, q.Get("band")):
		case q.Get("network") != "" && c.Network != q.Get("network"):
		case qs != "" && !strings.Contains(strings.ToLower(c.DisplayName+" "+c.Name+" "+c.MAC+" "+c.IP), qs):
		default:
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) client(w http.ResponseWriter, r *http.Request) {
	mac := deco.NormalizeMAC(pathParam(r, "mac"))
	since, err := s.since(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	all, err := s.clientViews(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	i := slices.IndexFunc(all, func(c clientView) bool { return c.MAC == mac })
	if i < 0 {
		writeError(w, http.StatusNotFound, "no such client")
		return
	}
	hist, err := s.store.ClientHistory(r.Context(), runOf(r.Context()).Network.ID, mac, since)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": all[i], "history": hist})
}

func (s *Server) patchClient(w http.ResponseWriter, r *http.Request) {
	mac := deco.NormalizeMAC(pathParam(r, "mac"))
	var body struct {
		Alias *string  `json:"alias"`
		Watch *bool    `json:"watch"`
		Tags  []string `json:"tags"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Alias != nil && len(*body.Alias) > 64 || len(body.Tags) > 20 {
		writeError(w, http.StatusBadRequest, "alias up to 64 characters and up to 20 tags")
		return
	}
	c, err := s.store.UpdateClientMeta(r.Context(), runOf(r.Context()).Network.ID, mac, store.ClientMeta{Alias: body.Alias, Watch: body.Watch, Tags: body.Tags})
	s.audit(r.Context(), userFrom(r.Context()), "client.update", mac, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mac": c.MAC, "alias": c.Alias, "watch": c.Watch, "tags": c.Tags})
}

// wifiView is the Wi-Fi settings plus which fields Atlantis may change per
// network (only writes verified on a real Deco).
type wifiView struct {
	*deco.WirelessConfig
	Editable map[string][]string `json:"editable"`
}

func (s *Server) wifi(w http.ResponseWriter, r *http.Request) {
	run := runOf(r.Context())
	snap := run.Poller.Snapshot()
	if snap.Wireless == nil {
		writeError(w, http.StatusServiceUnavailable, "no Wi-Fi data yet")
		return
	}
	v := wifiView{WirelessConfig: snap.Wireless, Editable: map[string][]string{}}
	if ww, ok := run.Session.Client().(deco.WiFiWriter); ok {
		v.Editable = ww.Editable()
	}
	writeJSON(w, http.StatusOK, v)
}

// patchWiFi changes one Wi-Fi network. Fields not yet verified on a Deco
// answer 501.
func (s *Server) patchWiFi(w http.ResponseWriter, r *http.Request) {
	var p deco.WirelessPatch
	if !readJSON(w, r, &p) {
		return
	}
	run := runOf(r.Context())
	var bands []string
	err := run.Session.Do(r.Context(), func(ctx context.Context, c deco.Client) error {
		ww, ok := c.(deco.WiFiWriter)
		if !ok {
			return deco.ErrUnsupported
		}
		var err error
		bands, err = ww.SetNetwork(ctx, p)
		return err
	})
	target := "network=" + p.Network + " fields=" + strings.Join(p.Fields(), ",")
	if p.SSID != nil {
		target += " ssid=" + *p.SSID
	}
	s.audit(r.Context(), userFrom(r.Context()), "wifi.set", target, err)
	switch {
	case err == nil:
	case errors.Is(err, deco.ErrNotVerified):
		writeError(w, http.StatusNotImplemented, "not available yet: "+err.Error())
		return
	case errors.Is(err, deco.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, deco.ErrUnsupported), errors.Is(err, session.ErrClosed):
		s.fail(w, err)
		return
	default:
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	run.Poller.Refresh()
	writeJSON(w, http.StatusOK, map[string]any{"network": p.Network, "bands": bands})
}

func (s *Server) firmware(w http.ResponseWriter, r *http.Request) {
	fw := runOf(r.Context()).Poller.Snapshot().Firmware
	if fw == nil {
		fw = []deco.Firmware{}
	}
	writeJSON(w, http.StatusOK, fw)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.EventFilter{Type: q.Get("type")}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC 3339")
			return
		}
		f.Since = t
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	ev, err := s.store.Events(r.Context(), runOf(r.Context()).Network.ID, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ev == nil {
		ev = []store.Event{}
	}
	writeJSON(w, http.StatusOK, ev)
}

// settings shows the effective, non-secret configuration. Changing it is
// done in the YAML file or environment for now.
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	c := s.Config
	n, _, err := s.store.GetNetwork(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	p := s.Site.Poll()
	source := "config"
	if p.FromUI {
		source = "ui"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deco":    map[string]any{"transport": n.Transport, "host": n.Host, "port": n.Port, "email": n.Email},
		"poll":    map[string]any{"fast": p.Fast.String(), "slow": p.Slow.String(), "offline_after_misses": p.OfflineAfterMisses, "source": source},
		"store":   map[string]any{"raw_retention": c.Store.RawRetention.String(), "rollup_retention": c.Store.RollupRetention.String()},
		"metrics": map[string]any{"enabled": c.Metrics.Enabled, "path": c.Metrics.Path, "per_client": c.Metrics.PerClient},
		"notify":  map[string]any{"quiet_hours": c.Notify.QuietHours, "timezone": c.Notify.Timezone, "events": events.All},
		"version": s.Version,
	})
}

// putSettings changes the refresh intervals. Other settings stay in the
// YAML file and environment.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Poll *struct {
			Fast               string `json:"fast"`
			Slow               string `json:"slow"`
			OfflineAfterMisses int    `json:"offline_after_misses"`
		} `json:"poll"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Poll == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: send poll")
		return
	}
	fast, err1 := time.ParseDuration(body.Poll.Fast)
	slow, err2 := time.ParseDuration(body.Poll.Slow)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, `intervals are durations such as "30s" or "5m"`)
		return
	}
	p := site.Poll{Fast: fast, Slow: slow, OfflineAfterMisses: body.Poll.OfflineAfterMisses}
	err := s.Site.SetPoll(r.Context(), p)
	s.audit(r.Context(), userFrom(r.Context()), "settings.poll", fmt.Sprintf("fast=%s slow=%s offline_after=%d", fast, slow, p.OfflineAfterMisses), err)
	if errors.Is(err, site.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.settings(w, r)
}

// channelView never includes the sealed config; secrets are write-only.
type channelView struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Events      []string `json:"events"`
	WatchedOnly bool     `json:"watched_only"`
	Template    string   `json:"template"`
	Configured  bool     `json:"configured"`
}

func viewChannel(c store.Channel) channelView {
	ev := c.Events
	if ev == nil {
		ev = []string{}
	}
	return channelView{ID: c.ID, Kind: c.Kind, Name: c.Name, Enabled: c.Enabled, Events: ev,
		WatchedOnly: c.WatchedOnly, Template: c.Template, Configured: c.ConfigEnc != ""}
}

type channelBody struct {
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	Enabled     *bool          `json:"enabled"`
	Events      []string       `json:"events"`
	WatchedOnly bool           `json:"watched_only"`
	Template    string         `json:"template"`
	Config      *notify.Config `json:"config"` // write-only
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := s.store.Channels(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]channelView, 0, len(chs))
	for _, c := range chs {
		out = append(out, viewChannel(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// buildChannel validates a body into a channel, keeping the stored secret
// config when the body omits it.
func (s *Server) buildChannel(b channelBody, cur store.Channel) (store.Channel, error) {
	c := cur
	if b.Kind != "" {
		c.Kind = b.Kind
	}
	if c.Kind != notify.KindShoutrrr && c.Kind != notify.KindOpenWA {
		return c, fmt.Errorf("kind must be %s or %s", notify.KindShoutrrr, notify.KindOpenWA)
	}
	if b.Name = strings.TrimSpace(b.Name); b.Name != "" {
		c.Name = b.Name
	}
	if c.Name == "" {
		return c, errors.New("name is required")
	}
	for _, e := range b.Events {
		if !slices.Contains(events.All, e) {
			return c, fmt.Errorf("unknown event %q", e)
		}
	}
	c.Events, c.WatchedOnly, c.Template = b.Events, b.WatchedOnly, b.Template
	if b.Enabled != nil {
		c.Enabled = *b.Enabled
	} else if cur.ID == 0 {
		c.Enabled = true
	}
	if _, err := notify.Render(c.Template, notify.Message{Type: "test"}); err != nil {
		return c, err
	}
	if b.Config != nil {
		b.Config.URL, b.Config.APIKey, b.Config.Chat = strings.TrimSpace(b.Config.URL), strings.TrimSpace(b.Config.APIKey), strings.TrimSpace(b.Config.Chat)
		enc, err := s.Notify.SealConfig(*b.Config)
		if err != nil {
			return c, err
		}
		c.ConfigEnc = enc
	}
	if c.ConfigEnc == "" {
		return c, errors.New("config is required")
	}
	return c, nil
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var b channelBody
	if !readJSON(w, r, &b) {
		return
	}
	c, err := s.buildChannel(b, store.Channel{})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err = s.store.SaveChannel(r.Context(), c)
	s.audit(r.Context(), userFrom(r.Context()), "channel.create", c.Name, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, viewChannel(c))
}

// pathParam returns a decoded path parameter. chi matches on the raw path,
// so a client that escapes ':' in a MAC (aa%3Abb...) would otherwise get
// the escaped form.
func pathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

func channelID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := channelID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad channel id")
		return
	}
	cur, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var b channelBody
	if !readJSON(w, r, &b) {
		return
	}
	c, err := s.buildChannel(b, cur)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err = s.store.SaveChannel(r.Context(), c)
	s.audit(r.Context(), userFrom(r.Context()), "channel.update", c.Name, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewChannel(c))
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := channelID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad channel id")
		return
	}
	err = s.store.DeleteChannel(r.Context(), id)
	s.audit(r.Context(), userFrom(r.Context()), "channel.delete", strconv.FormatInt(id, 10), err)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	id, err := channelID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad channel id")
		return
	}
	c, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.sendTest(w, r, c)
}

// testDraftChannel sends a test with unsaved settings, so a channel can be
// validated before it is saved.
func (s *Server) testDraftChannel(w http.ResponseWriter, r *http.Request) {
	var b channelBody
	if !readJSON(w, r, &b) {
		return
	}
	if b.Name == "" {
		b.Name = "test"
	}
	c, err := s.buildChannel(b, store.Channel{})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.sendTest(w, r, c)
}

func (s *Server) sendTest(w http.ResponseWriter, r *http.Request, c store.Channel) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.Notify.Test(ctx, c); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) sessionStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, runOf(r.Context()).Session.Status())
}

// releaseSession logs out of the Deco for ?minutes= (default 10) so the
// Deco app can be used, then resumes by itself.
func (s *Server) releaseSession(w http.ResponseWriter, r *http.Request) {
	mins := 10
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 24*60 {
			mins = n
		}
	}
	err := runOf(r.Context()).Session.Release(r.Context(), time.Duration(mins)*time.Minute)
	s.audit(r.Context(), userFrom(r.Context()), "session.release", fmt.Sprintf("%dm", mins), err)
	s.Hub.Publish(poller.Message{Kind: "session", Data: runOf(r.Context()).Session.Status()})
	writeJSON(w, http.StatusOK, runOf(r.Context()).Session.Status())
}

func (s *Server) pauseSession(w http.ResponseWriter, r *http.Request) {
	err := runOf(r.Context()).Session.Pause(r.Context())
	s.audit(r.Context(), userFrom(r.Context()), "session.pause", "", err)
	s.Hub.Publish(poller.Message{Kind: "session", Data: runOf(r.Context()).Session.Status()})
	writeJSON(w, http.StatusOK, runOf(r.Context()).Session.Status())
}

func (s *Server) resumeSession(w http.ResponseWriter, r *http.Request) {
	runOf(r.Context()).Session.Resume()
	s.audit(r.Context(), userFrom(r.Context()), "session.resume", "", nil)
	runOf(r.Context()).Poller.Refresh()
	writeJSON(w, http.StatusOK, runOf(r.Context()).Session.Status())
}

func (s *Server) acceptHostKey(w http.ResponseWriter, r *http.Request) {
	run := runOf(r.Context())
	err := s.Site.AcceptHostKey(r.Context())
	s.audit(r.Context(), userFrom(r.Context()), "session.accept_host_key", run.Network.Host, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	cur := s.Site.Current()
	if cur == nil {
		s.fail(w, site.ErrNoDeco)
		return
	}
	writeJSON(w, http.StatusOK, cur.Session.Status())
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.AuditLog(r.Context(), 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	if a == nil {
		a = []store.Audit{}
	}
	writeJSON(w, http.StatusOK, a)
}

// stream is the live feed: a snapshot first, then updates, as SSE.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	msgs, cancel := s.Hub.Subscribe()
	defer cancel()
	send := func(m poller.Message) bool {
		b, err := json.Marshal(m.Data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Kind, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if run := s.Site.Current(); run != nil {
		send(poller.Message{Kind: "snapshot", Data: run.Poller.Snapshot()})
		send(poller.Message{Kind: "session", Data: run.Session.Status()})
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-msgs:
			if !ok || !send(m) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// debugRaw returns a redacted raw reply (only when debug.enabled).
func (s *Server) debugRaw(w http.ResponseWriter, r *http.Request) {
	area, form := r.URL.Query().Get("area"), r.URL.Query().Get("form")
	var raw json.RawMessage
	err := runOf(r.Context()).Session.Do(r.Context(), func(ctx context.Context, c deco.Client) error {
		var err error
		raw, err = c.Raw(ctx, area, form)
		return err
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}
