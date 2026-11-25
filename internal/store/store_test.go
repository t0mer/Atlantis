package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
)

func open(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := t.TempDir() + "/a.db"
	for range 2 {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestNetworkLifecycle(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	n, err := s.EnsureNetwork(ctx, Network{Host: "192.0.2.1", Transport: "ssh", Email: "a@example.com", PasswordEnc: "v1:x"})
	if err != nil || n.ID == 0 {
		t.Fatal(n, err)
	}
	n2, _ := s.EnsureNetwork(ctx, Network{Host: "192.0.2.2", Transport: "ssh", Email: "a@example.com"})
	if n2.ID != n.ID || n2.Host != "192.0.2.2" || n2.PasswordEnc != "v1:x" {
		t.Fatalf("restart must keep the stored password and follow the config host: %+v", n2)
	}
	s.SetHostKey(ctx, n.ID, "SHA256:abc")
	n3, _ := s.EnsureNetwork(ctx, Network{Host: "192.0.2.2", Transport: "ssh"})
	if n3.HostKey != "SHA256:abc" {
		t.Fatal("host key lost")
	}
}

func TestNodesAndClients(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	n, _ := s.EnsureNetwork(ctx, Network{Host: "h", Transport: "ssh"})
	nodes := []deco.Device{{ID: "m", Master: true, Online: true, Nickname: "Main"}, {ID: "s", Online: true, Backhaul: deco.Backhaul{Type: "wifi"}}}
	if err := s.SaveNodes(ctx, n.ID, nodes); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	s.SaveNodes(ctx, n.ID, nodes[:1]) // satellite vanished
	got, _ := s.Nodes(ctx, n.ID)
	if len(got) != 2 || !got[0].Master || got[1].Online || got[1].Backhaul != "wifi" {
		t.Fatalf("%+v", got)
	}

	cl := []deco.WifiClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Phone", NodeID: "m", Band: "band5_1", Online: true}}
	if err := s.SaveClients(ctx, n.ID, cl, map[string]bool{"aa:bb:cc:dd:ee:01": true}); err != nil {
		t.Fatal(err)
	}
	alias, watch := "Kid's phone", true
	c, err := s.UpdateClientMeta(ctx, n.ID, "aa:bb:cc:dd:ee:01", ClientMeta{Alias: &alias, Watch: &watch, Tags: []string{"kids"}})
	if err != nil || c.DisplayName() != "Kid's phone" || !c.Watch || c.Tags[0] != "kids" {
		t.Fatalf("%+v %v", c, err)
	}
	// Going offline keeps the last node/band and the user's metadata.
	s.SaveClients(ctx, n.ID, nil, map[string]bool{"aa:bb:cc:dd:ee:01": false})
	c, _ = s.GetClient(ctx, n.ID, "aa:bb:cc:dd:ee:01")
	if c.Online || c.LastBand != "band5_1" || c.Alias != "Kid's phone" {
		t.Fatalf("%+v", c)
	}
	if _, err := s.GetClient(ctx, n.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	known, _ := s.KnownClients(ctx, n.ID)
	if !known["aa:bb:cc:dd:ee:01"] {
		t.Fatal("known clients")
	}
}

func TestHistoryAndCompaction(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	n, _ := s.EnsureNetwork(ctx, Network{Host: "h", Transport: "ssh"})
	start := *now
	for i := range 6 { // six samples, 20 minutes apart, over two hours
		*now = start.Add(time.Duration(i) * 20 * time.Minute)
		s.AddSamples(ctx, n.ID, []NodeSample{{NodeID: "m", Online: true, Clients: i}},
			[]ClientSample{{MAC: "c", Online: i%2 == 0, UpBps: int64(i * 1000)}})
	}
	pts, _ := s.ClientHistory(ctx, n.ID, "c", start)
	if len(pts) != 6 {
		t.Fatalf("raw history: %d", len(pts))
	}
	*now = start.Add(9 * 24 * time.Hour) // everything is past the 7-day raw retention
	if err := s.Compact(ctx, 7*24*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	pts, _ = s.ClientHistory(ctx, n.ID, "c", start)
	if len(pts) != 2 || pts[0].Online != 2.0/3 || pts[0].UpBps != 1000 {
		t.Fatalf("rollups: %+v", pts)
	}
	np, _ := s.NodeHistory(ctx, n.ID, "m", start)
	if len(np) != 2 || np[1].Clients != 4 {
		t.Fatalf("node rollups: %+v", np)
	}
	if err := s.Compact(ctx, 7*24*time.Hour, 90*24*time.Hour); err != nil { // idempotent
		t.Fatal(err)
	}
	if pts, _ = s.ClientHistory(ctx, n.ID, "c", start); len(pts) != 2 {
		t.Fatal("second compaction changed rollups")
	}
}

func TestEventsChannelsAuditSettings(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	n, _ := s.EnsureNetwork(ctx, Network{Host: "h", Transport: "ssh"})
	s.AddEvent(ctx, n.ID, Event{Type: "client.joined", Subject: "c1"})
	*now = now.Add(time.Second)
	s.AddEvent(ctx, n.ID, Event{Type: "node.offline", Subject: "s"})
	ev, _ := s.Events(ctx, n.ID, EventFilter{Type: "client."})
	if len(ev) != 1 || ev[0].Subject != "c1" {
		t.Fatalf("%+v", ev)
	}
	all, _ := s.Events(ctx, n.ID, EventFilter{})
	if len(all) != 2 || all[0].Type != "node.offline" {
		t.Fatal("newest first")
	}

	ch, _ := s.SaveChannel(ctx, Channel{Kind: "shoutrrr", Name: "tg", ConfigEnc: "v1:x", Enabled: true, Events: []string{"node.offline"}})
	ch.Enabled = false
	s.SaveChannel(ctx, ch)
	got, _ := s.GetChannel(ctx, ch.ID)
	if got.Enabled || got.Events[0] != "node.offline" {
		t.Fatalf("%+v", got)
	}
	if s.DeleteChannel(ctx, ch.ID) != nil || !errors.Is(s.DeleteChannel(ctx, ch.ID), ErrNotFound) {
		t.Fatal("delete")
	}

	s.AddAudit(ctx, Audit{User: "admin", Action: "wifi.set", Target: "host", Result: "ok"})
	if a, _ := s.AuditLog(ctx, 10); len(a) != 1 || a[0].Action != "wifi.set" {
		t.Fatal("audit")
	}

	type poll struct{ Fast int }
	s.SetSetting(ctx, "poll", poll{Fast: 30})
	var p poll
	if ok, _ := s.GetSetting(ctx, "poll", &p); !ok || p.Fast != 30 {
		t.Fatal("settings")
	}
	if ok, _ := s.GetSetting(ctx, "missing", &p); ok {
		t.Fatal("missing setting reported set")
	}
}

func TestFirstUserAndSessions(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	u, err := s.CreateFirstUser(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFirstUser(ctx, "evil", "hash"); !errors.Is(err, ErrUserExists) {
		t.Fatal("first-run setup replayed")
	}
	s.CreateSession(ctx, Session{TokenHash: "t", UserID: u.ID, CSRF: "c", ExpiresAt: now.Add(time.Hour)})
	if ss, err := s.GetSession(ctx, "t"); err != nil || ss.Username != "admin" || ss.CSRF != "c" {
		t.Fatal(ss, err)
	}
	*now = now.Add(2 * time.Hour)
	if _, err := s.GetSession(ctx, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session accepted")
	}
}

// Review finding: merging two batches into one hour keeps CPU/mem averages.
func TestRollupMergeKeepsLoad(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	n, _ := s.EnsureNetwork(ctx, Network{Host: "h", Transport: "ssh"})
	hour := *now
	cpu1, cpu2 := 0.2, 0.6
	s.AddSamples(ctx, n.ID, []NodeSample{{NodeID: "m", Online: true, CPU: &cpu1, Mem: &cpu1}}, nil)
	*now = hour.Add(9 * 24 * time.Hour)
	s.Compact(ctx, 7*24*time.Hour, 90*24*time.Hour)
	*now = hour.Add(10 * time.Minute) // the clock jumps back: a second batch for the same hour
	s.AddSamples(ctx, n.ID, []NodeSample{{NodeID: "m", Online: true, CPU: &cpu2, Mem: &cpu2}}, nil)
	*now = hour.Add(9 * 24 * time.Hour)
	s.Compact(ctx, 7*24*time.Hour, 90*24*time.Hour)
	pts, _ := s.NodeHistory(ctx, n.ID, "m", hour.Add(-time.Hour))
	if len(pts) != 1 || pts[0].CPU == nil || *pts[0].CPU < 0.39 || *pts[0].CPU > 0.41 {
		t.Fatalf("%+v", pts)
	}
}

func TestAddAndRemoveNetwork(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, ok, err := s.GetNetwork(ctx); ok || err != nil {
		t.Fatalf("a new database has no Deco: ok=%v err=%v", ok, err)
	}
	n, err := s.SaveNetwork(ctx, Network{Host: "192.0.2.1", Port: 20001, Transport: "ssh", Email: "a@example.com", PasswordEnc: "v1:x"})
	if err != nil {
		t.Fatal(err)
	}
	n.Host, n.PasswordEnc = "192.0.2.9", "v1:y"
	if _, err := s.SaveNetwork(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.GetNetwork(ctx)
	if !ok || got.ID != n.ID || got.Host != "192.0.2.9" || got.Port != 20001 || got.PasswordEnc != "v1:y" {
		t.Fatalf("%+v", got)
	}

	s.SaveNodes(ctx, n.ID, []deco.Device{{ID: "m", Master: true, Online: true}})
	s.SaveClients(ctx, n.ID, []deco.WifiClient{{MAC: "aa:bb:cc:dd:ee:01", Online: true}}, map[string]bool{"aa:bb:cc:dd:ee:01": true})
	s.AddSamples(ctx, n.ID, []NodeSample{{NodeID: "m", Online: true}}, []ClientSample{{MAC: "aa:bb:cc:dd:ee:01", Online: true}})
	s.AddEvent(ctx, n.ID, Event{Type: "client.joined", Subject: "aa:bb:cc:dd:ee:01"})
	s.SetSetting(ctx, "poll", map[string]int{"fast": 30})

	if err := s.DeleteNetwork(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range append(networkTables, "networks") {
		var k int
		s.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&k)
		if k != 0 {
			t.Errorf("%s still has %d rows", tbl, k)
		}
	}
	if ok, _ := s.GetSetting(ctx, "poll", new(map[string]int)); !ok {
		t.Error("settings are not per site and must stay")
	}
}
