package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	b, _ := io.ReadAll(rr.Body)
	return string(b)
}

func TestMetrics(t *testing.T) {
	m := New("watched")
	m.Nodes([]deco.Device{{MAC: "aa:00", Nickname: "Main", Model: "M5", Online: true, Master: true, InternetOnline: true}})
	m.Clients([]Client{
		{WifiClient: deco.WifiClient{MAC: "c1", Online: true, NodeID: "m", Band: "band5_1", Network: "main", DownBps: 8000}, DisplayName: "Kid", Watch: true},
		{WifiClient: deco.WifiClient{MAC: "c2", Online: true, NodeID: "m", Wired: true, Network: "main"}, DisplayName: "TV"},
	})
	m.Poll("fast", 200*time.Millisecond, "")
	m.Poll("slow", time.Second, "session")
	out := scrape(t, m)
	for _, want := range []string{
		`deco_node_up{mac="aa:00",model="M5",name="Main"} 1`,
		`deco_wan_up 1`,
		`deco_clients{band="band5_1",interface="main",node="m"} 1`,
		`deco_clients{band="wired",interface="main",node="m"} 1`,
		`deco_client_up{mac="c1",name="Kid"} 1`,
		`deco_client_rx_bytes_rate{mac="c1",name="Kid"} 1000`,
		`deco_poll_errors_total{loop="slow",reason="session"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out, `name="TV"`) {
		t.Error("an unwatched client got per-client series")
	}
	if strings.Contains(out, "deco_cpu_usage 0") == false && strings.Contains(out, "deco_cpu_usage") == false {
		t.Error("cpu gauge not registered")
	}
	m.Nodes(nil)
	if strings.Contains(scrape(t, m), `deco_node_up{`) {
		t.Error("a vanished node kept its series")
	}
}
