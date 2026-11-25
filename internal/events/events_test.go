package events

import (
	"reflect"
	"testing"

	"github.com/t0mer/atlantis/internal/deco"
)

func cl(mac string, online bool) deco.WifiClient { return deco.WifiClient{MAC: mac, Online: online} }

func types(ev []Event) []string {
	var out []string
	for _, e := range ev {
		out = append(out, e.Type+":"+e.Subject)
	}
	return out
}

func TestClientEvents(t *testing.T) {
	cases := []struct {
		name   string
		known  map[string]bool
		polls  [][]deco.WifiClient
		want   [][]string // events per poll
		online map[string]bool
	}{
		{
			name:  "fresh store: first poll is a baseline, later MACs join",
			polls: [][]deco.WifiClient{{cl("a", true)}, {cl("a", true), cl("b", true)}},
			want:  [][]string{nil, {"client.joined:b"}},
		},
		{
			name:  "known store: an unknown MAC on the first poll joins (no online on the baseline poll)",
			known: map[string]bool{"a": true},
			polls: [][]deco.WifiClient{{cl("a", true), cl("n", true)}},
			want:  [][]string{{"client.joined:n"}},
		},
		{
			name:  "offline only after 2 missed polls, online again at once",
			known: map[string]bool{"a": true},
			polls: [][]deco.WifiClient{{cl("a", true)}, {}, {}, {}, {cl("a", true)}},
			want:  [][]string{nil, nil, {"client.offline:a"}, nil, {"client.online:a"}},
		},
		{
			name:  "reported offline counts as missed; a blip does not fire",
			known: map[string]bool{"a": true},
			polls: [][]deco.WifiClient{{cl("a", true)}, {cl("a", false)}, {cl("a", true)}, {cl("a", false)}, {cl("a", false)}},
			want:  [][]string{nil, nil, nil, nil, {"client.offline:a"}},
		},
		{
			name:  "a known client absent at start comes online later",
			known: map[string]bool{"a": true, "z": true},
			polls: [][]deco.WifiClient{{cl("a", true)}, {cl("a", true), cl("z", true)}},
			want:  [][]string{nil, {"client.online:z"}},
		},
		{
			name:   "offline at start stays quiet",
			known:  map[string]bool{"a": true},
			polls:  [][]deco.WifiClient{{cl("a", false)}, {cl("a", false)}},
			want:   [][]string{nil, nil},
			online: map[string]bool{"a": false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New(tc.known, 2)
			var presence map[string]bool
			for i, p := range tc.polls {
				var ev []Event
				ev, presence = e.Clients(p)
				if !reflect.DeepEqual(types(ev), tc.want[i]) {
					t.Fatalf("poll %d: got %v, want %v", i, types(ev), tc.want[i])
				}
			}
			if tc.online != nil && !reflect.DeepEqual(presence, tc.online) {
				t.Fatalf("presence %v", presence)
			}
		})
	}
}

func node(id string, online, master, inet bool, fw string) deco.Device {
	return deco.Device{ID: id, Online: online, Master: master, InternetOnline: inet, SoftwareVer: fw}
}

func TestNodeWANAndFirmwareEvents(t *testing.T) {
	e := New(nil, 2)
	steps := []struct {
		nodes []deco.Device
		want  []string
	}{
		{[]deco.Device{node("m", true, true, true, "1.0"), node("s", true, false, true, "1.0")}, nil},
		{[]deco.Device{node("m", true, true, false, "1.0"), node("s", false, false, false, "1.0")}, []string{"node.offline:s", "wan.down:wan"}},
		{[]deco.Device{node("m", true, true, true, "1.1")}, []string{"firmware.changed:m", "wan.up:wan"}},
		{[]deco.Device{node("m", true, true, true, "1.1"), node("s", true, false, true, "1.0")}, []string{"node.online:s"}},
		{[]deco.Device{node("m", true, true, true, "1.1")}, []string{"node.offline:s"}}, // vanished from the list
		{[]deco.Device{node("m", true, true, true, "1.1"), node("new", true, false, true, "")}, []string{"node.online:new"}},
	}
	for i, st := range steps {
		if got := types(e.Nodes(st.nodes)); !reflect.DeepEqual(got, st.want) {
			t.Fatalf("step %d: got %v, want %v", i, got, st.want)
		}
	}
}

func TestWANIPChange(t *testing.T) {
	e := New(nil, 2)
	if ev := e.WAN(&deco.WANStatus{IP: "203.0.113.1"}); ev != nil {
		t.Fatal("first IP is a baseline")
	}
	if ev := e.WAN(&deco.WANStatus{IP: "203.0.113.1"}); ev != nil {
		t.Fatal("no change")
	}
	ev := e.WAN(&deco.WANStatus{IP: "203.0.113.9"})
	if len(ev) != 1 || ev[0].Type != WANIPChanged || ev[0].Payload["from"] != "203.0.113.1" {
		t.Fatalf("%+v", ev)
	}
	if e.WAN(nil) != nil || e.WAN(&deco.WANStatus{}) != nil {
		t.Fatal("unsupported WAN must be quiet")
	}
}
