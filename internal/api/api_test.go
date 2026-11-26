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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/metrics"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/poller"
	"github.com/t0mer/atlantis/internal/prefs"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/site"
	"github.com/t0mer/atlantis/internal/store"
)

type fakeDeco struct {
	mu       sync.Mutex
	ssid     string
	wifiSets int
	renames  []string // "mac=name"
}

func (f *fakeDeco) CanRenameClients() bool { return true }
func (f *fakeDeco) RenameClient(_ context.Context, mac, name string) error {
	if deco.NormalizeMAC(mac) != "aa:bb:cc:dd:ee:01" {
		return deco.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, mac+"="+name)
	return nil
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
func (f *fakeDeco) Editable() map[string][]string {
	return map[string][]string{"host": {deco.FieldSSID, deco.FieldPassword}}
}
func (f *fakeDeco) SetNetwork(_ context.Context, p deco.WirelessPatch) ([]string, error) {
	for _, fl := range p.Fields() {
		if !slices.Contains(f.Editable()[p.Network], fl) {
			return nil, deco.ErrNotVerified
		}
	}
	if p.SSID != nil && *p.SSID == "" {
		return nil, deco.Invalid("SSID must be 1-32 bytes of text")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.SSID != nil {
		f.ssid = *p.SSID
	}
	f.wifiSets++
	return []string{"band2_4"}, nil
}

type env struct {
	srv  *httptest.Server
	fake *fakeDeco
	st   *store.Store
	box  *secret.Box
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
	box, _ := secret.New(secret.NewKey())
	sealed, _ := box.Seal("decopass")
	st.SaveNetwork(ctx, store.Network{Host: "192.0.2.1", Transport: "ssh", Email: "owner@example.com", PasswordEnc: sealed})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fakeDeco{ssid: "old"}
	hub := poller.NewHub()
	disp := notify.New(st, box, func() string { return "Home" }, log)
	m := metrics.New("all")
	sctx, cancel := context.WithCancel(ctx)
	sites := site.New(sctx, site.Deps{Poll: site.Poll{Fast: time.Minute, Slow: time.Minute, OfflineAfterMisses: 2}, Store: st, Box: box, Hub: hub, Notify: disp, Metrics: m, Log: log,
		Build: func(context.Context, store.Network, string, *site.HostKeys, func(string), *slog.Logger) (deco.Client, *site.Discovery, error) {
			return f, &site.Discovery{Model: "M5", Role: "master", Port: 20001}, nil
		}})
	if err := sites.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sites.Close(context.Background()) })
	// Run polls once straight away; wait for that first pass.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if snap := sites.Current().Poller.Snapshot(); !snap.FastAt.IsZero() && !snap.SlowAt.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first poll did not finish")
		}
	}
	pf := prefs.New(st, box)
	if err := pf.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pf.Update(ctx, prefs.Patch{Debug: &prefs.DebugPrefs{Enabled: true},
		Metrics: &prefs.MetricsPatch{Enabled: true, Path: "/metrics", PerClient: "all"}}); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Store: st, Site: sites, Prefs: pf, Hub: hub, Notify: disp, Metrics: m.Handler(), Version: "test", Log: log})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &env{srv: srv, fake: f, st: st, box: box, cl: &http.Client{Jar: jar}}
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
	// A cross-site form can only send text/plain; it must not reach setup.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/auth/setup", strings.NewReader(`{"username":"evil","password":"aaaaaaaaaa="}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, err := e.cl.Do(req); err != nil || resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain setup: %v %v", resp, err)
	} else {
		resp.Body.Close()
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
	if !strings.Contains(nodes, `"nickname":"Main"`) || !strings.Contains(nodes, `"clients":1`) {
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
		t.Fatal("metrics for a logged-in user")
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
	// Browsers' encodeURIComponent escapes the colons; the route must still match.
	if code, _, raw := e.do(t, "GET", "/api/v1/clients/aa%3Abb%3Acc%3Add%3Aee%3A01", "", false); code != 200 || !strings.Contains(raw, "Kid's phone") {
		t.Fatalf("escaped MAC: %d %s", code, raw)
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
	if code, _, _ := e.do(t, "PATCH", "/api/v1/wifi", `{"bands":["band2_4"],"network":"guest","enable":true}`, true); code != 501 {
		t.Fatal("unverified Wi-Fi toggle must answer 501")
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/wifi", `{"network":"host","ssid":""}`, true); code != 400 {
		t.Fatal("an invalid change must answer 400")
	}
	_, w, _ := e.do(t, "GET", "/api/v1/wifi", "", false)
	if ed, _ := w["editable"].(map[string]any); len(ed) != 1 || ed["host"] == nil {
		t.Fatalf("editable: %v", w["editable"])
	}
	if code, _, _ := e.do(t, "PATCH", "/api/v1/wifi", `{"network":"guest","ssid":"x","password":"longenough"}`, true); code != 501 {
		t.Fatal("writes to other networks must answer 501")
	}
	code, m, raw := e.do(t, "PATCH", "/api/v1/wifi", `{"network":"host","ssid":"Test-SSID","password":"test-pass-123"}`, true)
	if code != 200 || e.fake.wifiSets != 1 || e.fake.ssid != "Test-SSID" { // the rejected patches never reached the device
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, "test-pass-123") {
		t.Fatal("password echoed")
	}
	_ = m
	_, _, audit := e.do(t, "GET", "/api/v1/audit", "", false)
	if !strings.Contains(audit, "network=host fields=ssid,password ssid=Test-SSID") || strings.Contains(audit, "test-pass-123") {
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
	// OpenWA has no defaults: each channel needs its own address and chat.
	for _, cfg := range []string{`{"url":"http://wa.lan"}`, `{"chat":"123@c.us"}`} {
		if code, m, _ := e.do(t, "POST", "/api/v1/channels", `{"kind":"openwa","name":"wa","config":`+cfg+`}`, true); code != 400 || !strings.Contains(m["error"].(string), "address and the chat") {
			t.Fatalf("%s: %d %v", cfg, code, m)
		}
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/channels", `{"kind":"openwa","name":"wa","config":{"url":"http://wa.lan","chat":"123@c.us"}}`, true); code != 201 {
		t.Fatalf("a complete OpenWA channel: %d", code)
	}
	// A channel saved before OpenWA needed its own fields can still be
	// switched off or renamed (its config is not touched).
	sealed, _ := e.box.Seal(`{"url":"","chat":""}`)
	old, _ := e.st.SaveChannel(context.Background(), store.Channel{Kind: "openwa", Name: "old wa", ConfigEnc: sealed, Enabled: true})
	if code, _, _ := e.do(t, "PUT", "/api/v1/channels/"+itoa(int(old.ID)), `{"name":"old wa","enabled":false}`, true); code != 200 {
		t.Fatalf("switching off an old channel: %d", code)
	}
	// Changing the kind without a new config is checked against the new kind.
	if code, _, _ := e.do(t, "PUT", "/api/v1/channels/"+itoa(id), `{"kind":"openwa"}`, true); code != 400 {
		t.Fatalf("a Shoutrrr config under the OpenWA kind: %d", code)
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
	if m["state"] != "idle" && m["state"] != "connected" { // the poller may already have reconnected
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
	if len(lines) < 4 || lines[0] != "event: snapshot" || !strings.Contains(lines[1], `"nickname":"Main"`) || lines[2] != "event: session" {
		t.Fatalf("%v", lines)
	}
}

// The spec and the router must list the same routes.
func TestSpecMatchesRouter(t *testing.T) {
	s := New(Deps{Prefs: prefs.New(nil, nil), Log: slog.Default()})
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

// Metrics expose client names and MACs, so they are never public.
func TestMetricsNeedAuth(t *testing.T) {
	e := newEnv(t)
	resp, _ := http.Get(e.srv.URL + "/metrics") // no cookie
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("public metrics: %d", resp.StatusCode)
	}
}

func TestMetricsBearerToken(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secret.New(secret.NewKey())
	pf := prefs.New(st, box)
	tok := "scrape-me"
	if _, err := pf.Update(context.Background(), prefs.Patch{Metrics: &prefs.MetricsPatch{Enabled: true, Path: "/metrics", PerClient: "watched", Token: &tok}}); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Prefs: pf, Log: slog.Default(),
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })})
	for tok, want := range map[string]int{"scrape-me": 200, "wrong": 401, "": 401} {
		req := httptest.NewRequest("GET", "/metrics", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rr := httptest.NewRecorder()
		s.metricsAuth(s.Metrics).ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("token %q: %d, want %d", tok, rr.Code, want)
		}
	}
}

// Review finding: the stream must outlive the 60s request timeout.
func TestStreamIsNotTimedOut(t *testing.T) {
	var found bool
	s := New(Deps{Prefs: prefs.New(nil, nil), Log: slog.Default()})
	chi.Walk(s.Handler().(chi.Routes), func(method, route string, h http.Handler, mws ...func(http.Handler) http.Handler) error {
		if route == "/api/v1/stream" {
			found = true
			if len(mws) > 4 { // Recoverer, securityHeaders, metricsRoute, requireAuth (via With); a timeout would be a 5th
				t.Errorf("stream has %d middlewares; the timeout must not apply", len(mws))
			}
		}
		return nil
	})
	if !found {
		t.Fatal("stream route missing")
	}
}

// Review finding: parallel guesses must not exceed the limit.
func TestLoginLimitHoldsUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	unauthorized := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(e.srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"wrong"}`))
			if err == nil {
				if resp.StatusCode == 401 {
					mu.Lock()
					unauthorized++
					mu.Unlock()
				}
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if unauthorized > maxFailures {
		t.Fatalf("%d guesses were checked, limit is %d", unauthorized, maxFailures)
	}
}
