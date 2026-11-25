package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/session"
	"github.com/t0mer/atlantis/internal/store"
)

type fakeDeco struct {
	mu       sync.Mutex
	ssid     string
	wifiSets int
}

func (f *fakeDeco) Login(context.Context) error  { return nil }
func (f *fakeDeco) Logout(context.Context) error { return nil }
func (f *fakeDeco) Devices(context.Context) ([]deco.Device, error) {
	return []deco.Device{{ID: "m", MAC: "aa:bb:cc:00:00:01", Master: true, Online: true, InternetOnline: true, Nickname: "Main", Model: "M5"}}, nil
}
func (f *fakeDeco) Clients(context.Context, string) ([]deco.WifiClient, error) {
	return []deco.WifiClient{
		{MAC: "aa:bb:cc:dd:ee:01", Name: "Phone", Online: true, NodeID: "m", Band: "band5_1", Network: "main", DownBps: 8000},
		{MAC: "02:bb:cc:dd:ee:02", Name: "Randomised", Online: false, NodeID: "m"},
	}, nil
}
func (f *fakeDeco) WAN(context.Context) (*deco.WANStatus, error) { return nil, deco.ErrUnsupported }
func (f *fakeDeco) Performance(context.Context) (*deco.Performance, error) {
	return nil, deco.ErrUnsupported
}
func (f *fakeDeco) Wireless(context.Context) (*deco.WirelessConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &deco.WirelessConfig{Bands: map[string]deco.Band{"band2_4": {Networks: map[string]deco.NetworkConfig{"host": {Enable: true, SSID: f.ssid}}}}}, nil
}
func (f *fakeDeco) Firmware(context.Context) ([]deco.Firmware, error) { return nil, nil }
func (f *fakeDeco) Raw(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}
func (f *fakeDeco) SetHostWiFi(_ context.Context, ssid, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ssid, f.wifiSets = ssid, f.wifiSets+1
	return []string{"band2_4"}, nil
}

type env struct {
	srv  *httptest.Server
	fake *fakeDeco
	st   *store.Store
	cl   *http.Client
	csrf string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n, _ := st.EnsureNetwork(ctx, store.Network{Host: "192.0.2.1", Transport: "ssh"})
	cfg := &config.Config{Deco: config.Deco{Host: "192.0.2.1", Transport: "ssh"}, Poll: config.Poll{Fast: time.Minute, Slow: time.Minute, OfflineAfterMisses: 2},
		Store: config.Store{RawRetention: time.Hour, RollupRetention: 2 * time.Hour}, Metrics: config.Metrics{Path: "/metrics", PerClient: "all"},
		Notify: config.Notify{Timezone: "UTC"}, Debug: config.Debug{Enabled: true}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fakeDeco{ssid: "old"}
	sess := session.New(f, nil)
	hub := poller.NewHub()
	box, _ := secret.New(secret.NewKey())
	disp := notify.New(st, box, cfg, func() string { return "Home" }, log)
	m := metrics.New("all")
	p, err := poller.New(ctx, poller.Options{NetworkID: n.ID, Fast: time.Minute, Slow: time.Minute, OfflineAfter: 2, RawRetention: time.Hour, RollupRetention: 2 * time.Hour},
		sess, st, disp, m, hub, log)
	if err != nil {
		t.Fatal(err)
	}
	p.Slow(ctx)
	p.Fast(ctx)
	s := New(Deps{Config: cfg, Store: st, NetworkID: n.ID, Session: sess, Poller: p, Hub: hub, Notify: disp, Metrics: m.Handler(), Version: "test", Log: log,
		AcceptHostKey: func(context.Context) error { return nil }})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &env{srv: srv, fake: f, st: st, cl: &http.Client{Jar: jar}}
}

func (e *env) do(t *testing.T, method, path, body string, csrf bool) (int, map[string]any, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set(csrfHeader, e.csrf)
	}
	resp, err := e.cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func (e *env) login(t *testing.T) {
	t.Helper()
	code, m, raw := e.do(t, "POST", "/api/v1/auth/setup", `{"username":"admin","password":"correct horse battery"}`, false)
	if code != 200 {
		t.Fatalf("setup: %d %s", code, raw)
	}
	e.csrf = m["csrf_token"].(string)
}

func TestAuthFlow(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.do(t, "GET", "/health", "", false); code != 200 {
		t.Fatal("health must be public")
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/nodes", "", false); code != 401 {
		t.Fatal("API must need a login")
	}
	_, st, _ := e.do(t, "GET", "/api/v1/auth/status", "", false)
	if st["setup_required"] != true {
		t.Fatal("setup required")
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/auth/setup", `{"username":"admin","password":"short"}`, false); code != 400 {
		t.Fatal("weak password accepted")
	}
	e.login(t)
	if code, _, _ := e.do(t, "POST", "/api/v1/auth/setup", `{"username":"evil","password":"another long one"}`, false); code != 409 {
		t.Fatal("setup replayed")
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/nodes", "", false); code != 200 {
		t.Fatal("logged-in read failed")
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/session/pause", "", false); code != 403 {
		t.Fatal("mutation without CSRF accepted")
	}
	e.do(t, "POST", "/api/v1/auth/logout", "", false)
	if code, _, _ := e.do(t, "GET", "/api/v1/nodes", "", false); code != 401 {
		t.Fatal("logout did not end the session")
	}
	for range 5 {
		e.do(t, "POST", "/api/v1/auth/login", `{"username":"admin","password":"wrong"}`, false)
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/auth/login", `{"username":"admin","password":"correct horse battery"}`, false); code != 429 {
		t.Fatal("no lockout after repeated failures")
	}
}

func TestReadEndpoints(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_, net, raw := e.do(t, "GET", "/api/v1/network", "", false)
	if net["internet_online"] != true || net["wan_supported"] != false || net["clients"].(map[string]any)["band5"].(float64) != 1 {
		t.Fatalf("network: %s", raw)
	}
	_, _, nodes := e.do(t, "GET", "/api/v1/nodes", "", false)
	if !strings.Contains(nodes, `"Nickname":"Main"`) || !strings.Contains(nodes, `"clients":1`) {
		t.Fatalf("nodes: %s", nodes)
	}
	_, _, cl := e.do(t, "GET", "/api/v1/clients?online=true", "", false)
	if !strings.Contains(cl, "Phone") || strings.Contains(cl, "Randomised") {
		t.Fatalf("online filter: %s", cl)
	}
	_, _, all := e.do(t, "GET", "/api/v1/clients?q=random", "", false)
	if !strings.Contains(all, `"private_mac":true`) {
		t.Fatalf("search/private MAC: %s", all)
	}
	if code, m, _ := e.do(t, "GET", "/api/v1/clients/AA-BB-CC-DD-EE-01?range=7d", "", false); code != 200 || m["history"] == nil {
		t.Fatalf("client detail: %d %v", code, m)
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/nodes/m?range=1y", "", false); code != 400 {
		t.Fatal("bad range accepted")
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/nodes/nope", "", false); code != 404 {
		t.Fatal("unknown node")
	}
	if code, _, body := e.do(t, "GET", "/metrics", "", false); code != 200 || !strings.Contains(body, "deco_node_up") {
		t.Fatal("metrics")
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/debug/raw?area=tmp&form=DEVICE_LIST_GET", "", false); code != 200 {
		t.Fatal("debug raw")
	}
}

func TestClientMetaAndAudit(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	code, m, raw := e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"alias":"Kid's phone","watch":true,"tags":["kids"]}`, true)
	if code != 200 || m["alias"] != "Kid's phone" {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"nickname":"x"}`, true); code != 400 {
		t.Fatal("unknown field accepted")
	}
	_, _, audit := e.do(t, "GET", "/api/v1/audit", "", false)
	if !strings.Contains(audit, "client.update") {
		t.Fatalf("audit: %s", audit)
	}
}

func TestWritesAreGuarded(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	if code, _, _ := e.do(t, "POST", "/api/v1/nodes/m/reboot", "", true); code != 501 {
		t.Fatal("unverified reboot must answer 501")
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/wifi", `{"band":"band2_4","network":"guest","enable":true}`, true); code != 501 {
		t.Fatal("unverified Wi-Fi toggle must answer 501")
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/wifi", `{"network":"guest","ssid":"x","password":"longenough"}`, true); code != 400 {
		t.Fatal("only the host network may be changed")
	}
	code, m, raw := e.do(t, "PATCH", "/api/v1/wifi", `{"network":"host","ssid":"Test-SSID","password":"test-pass-123"}`, true)
	if code != 200 || e.fake.wifiSets != 1 || e.fake.ssid != "Test-SSID" {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, "test-pass-123") {
		t.Fatal("password echoed")
	}
	_ = m
	_, _, audit := e.do(t, "GET", "/api/v1/audit", "", false)
	if !strings.Contains(audit, "wifi.set_host") || strings.Contains(audit, "test-pass-123") {
		t.Fatalf("audit: %s", audit)
	}
}

func TestChannelsNeverReturnSecrets(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	code, m, raw := e.do(t, "POST", "/api/v1/channels", `{"kind":"shoutrrr","name":"tg","events":["node.offline"],"config":{"url":"telegram://SECRET@telegram?chats=1"}}`, true)
	if code != 201 || strings.Contains(raw, "SECRET") || m["configured"] != true {
		t.Fatalf("%d %s", code, raw)
	}
	id := int(m["id"].(float64))
	_, _, list := e.do(t, "GET", "/api/v1/channels", "", false)
	if strings.Contains(list, "SECRET") {
		t.Fatal("config leaked in list")
	}
	code, _, _ = e.do(t, "PUT", "/api/v1/channels/"+itoa(id), `{"name":"tg2","enabled":false}`, true)
	if code != 200 {
		t.Fatal("update without config must keep the stored one")
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/channels", `{"kind":"shoutrrr","name":"x","events":["bogus"],"config":{"url":"x"}}`, true); code != 400 {
		t.Fatal("unknown event accepted")
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/channels", `{"kind":"email","name":"x","config":{"url":"x"}}`, true); code != 400 {
		t.Fatal("unknown kind accepted")
	}
	if code, m, _ := e.do(t, "POST", "/api/v1/channels/test", `{"kind":"shoutrrr","config":{"url":"not-a-scheme://x"}}`, true); code != 502 || m["ok"] != false {
		t.Fatal("draft test of a broken URL must fail")
	}
	if code, _, _ := e.do(t, "DELETE", "/api/v1/channels/"+itoa(id), "", true); code != 204 {
		t.Fatal("delete")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestSessionControls(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_, m, _ := e.do(t, "POST", "/api/v1/session/logout?minutes=5", "", true)
	if m["state"] != "paused" || m["paused_for"] != "5m0s" {
		t.Fatalf("%v", m)
	}
	_, m, _ = e.do(t, "POST", "/api/v1/session/resume", "", true)
	if m["state"] != "idle" {
		t.Fatalf("%v", m)
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/session/accept-host-key", "", true); code != 200 {
		t.Fatal("accept host key")
	}
}

func TestStreamSendsSnapshot(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/api/v1/stream", nil)
	resp, err := e.cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var lines []string
	for sc.Scan() && len(lines) < 4 {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 4 || lines[0] != "event: snapshot" || !strings.Contains(lines[1], `"Nickname":"Main"`) || lines[2] != "event: session" {
		t.Fatalf("%v", lines)
	}
}

// The spec and the router must list the same routes.
func TestSpecMatchesRouter(t *testing.T) {
	s := New(Deps{Config: &config.Config{Metrics: config.Metrics{Path: "/metrics"}, Debug: config.Debug{Enabled: true}}, Log: slog.Default()})
	var routes []string
	chi.Walk(s.Handler().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/v1/") || route == "/health" {
			routes = append(routes, method+" "+strings.TrimSuffix(route, "/"))
		}
		return nil
	})
	var specRoutes []string
	path := ""
	pathRE := regexp.MustCompile(`^  (/\S+):$`)
	methodRE := regexp.MustCompile(`^    (get|post|put|patch|delete):`)
	for _, line := range strings.Split(string(spec), "\n") {
		if m := pathRE.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := methodRE.FindStringSubmatch(line); m != nil && path != "" {
			specRoutes = append(specRoutes, strings.ToUpper(m[1])+" "+path)
		}
	}
	sort.Strings(routes)
	sort.Strings(specRoutes)
	if strings.Join(routes, "\n") != strings.Join(specRoutes, "\n") {
		t.Fatalf("router:\n%s\n\nspec:\n%s", strings.Join(routes, "\n"), strings.Join(specRoutes, "\n"))
	}
}
