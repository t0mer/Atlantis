package main

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/store"
)

// schedule says when a pretend client is home, as local hour ranges
// [from, to). Clients without one are always online.
var schedule = map[string][][2]float64{
	"Dana's iPhone":     {{0, 8.5}, {17.5, 24}},
	"הטלפון של נועה":    {{0, 7.75}, {14, 24}},
	"MacBook Pro":       {{8.75, 13}, {13.5, 19.25}},
	"Kids' laptop":      {{15, 20}},
	"iPad":              {{19, 22.5}},
	"Living room TV":    {{7, 8}, {18.5, 23.5}},
	"Robot vacuum":      {{10, 11.5}},
	"Guest phone":       {{-1, -1}}, // the last three hours only, below
	"0a:11:22:33:44:55": {{-1, -1}},
}

func home(c *client, t, now time.Time) bool {
	key := c.name
	if key == "" {
		key = c.mac
	}
	ranges, ok := schedule[key]
	if !ok {
		return true
	}
	if ranges[0][0] < 0 {
		return now.Sub(t) < 3*time.Hour
	}
	h := float64(t.Hour()) + float64(t.Minute())/60
	for _, r := range ranges {
		if h >= r[0] && h < r[1] {
			return true
		}
	}
	return false
}

// seedHistory writes a day of five-minute samples and the matching
// events into a fresh database, so the UI's strips and charts have
// something to show. The garage Deco drops off for 20 minutes.
func seedHistory(path string, span time.Duration) error {
	ctx := context.Background()
	st, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer st.Close()
	netw, err := st.EnsureNetwork(ctx, store.Network{Transport: "ssh"})
	if err != nil {
		return err
	}
	now := time.Now().Truncate(5 * time.Minute)
	outFrom, outTo := now.Add(-7*time.Hour), now.Add(-6*time.Hour-40*time.Minute)
	rnd := rand.New(rand.NewPCG(1, 2))
	prev := map[string]bool{}
	event := func(t time.Time, typ, subject string, payload map[string]any) error {
		b, _ := json.Marshal(payload)
		_, err := st.AddEvent(ctx, netw.ID, store.Event{TS: t, Type: typ, Subject: subject, Payload: b})
		return err
	}
	for t := now.Add(-span); !t.After(now); t = t.Add(5 * time.Minute) {
		st.SetClock(func() time.Time { return t })
		garageUp := t.Before(outFrom) || !t.Before(outTo)
		perNode := map[string]int{}
		var cs []store.ClientSample
		var known []deco.WifiClient
		for _, c := range clients {
			on := home(c, t, now) && (garageUp || c.node != "node-garage")
			s := store.ClientSample{MAC: c.mac, NodeID: c.node, Band: c.band, Network: c.network, IP: c.ip, Online: on}
			if on {
				perNode[c.node]++
				jit := 0.3 + 1.4*rnd.Float64()
				s.DownBps, s.UpBps = int64(float64(c.down)*jit*1000), int64(float64(c.up)*jit*1000)
			}
			cs = append(cs, s)
			was, seen := prev[c.mac]
			if seen && was != on {
				typ := "client.offline"
				if on {
					typ = "client.online"
				}
				if err := event(t, typ, c.mac, map[string]any{"name": c.name}); err != nil {
					return err
				}
			}
			prev[c.mac] = on
			known = append(known, deco.WifiClient{MAC: c.mac, IP: c.ip, Name: c.name, Type: c.typ, Network: c.network,
				Wired: c.wired, Band: c.band, NodeID: c.node, Online: on})
		}
		var ns []store.NodeSample
		for _, n := range nodes {
			id := n["device_id"].(string)
			up := id != "node-garage" || garageUp
			ns = append(ns, store.NodeSample{NodeID: id, Online: up, InternetOnline: true, Clients: perNode[id], Signal: 3})
		}
		if err := st.AddSamples(ctx, netw.ID, ns, cs); err != nil {
			return err
		}
		if t.Equal(outFrom) || t.Equal(outTo) {
			typ := map[bool]string{true: "node.online", false: "node.offline"}[garageUp]
			if err := event(t, typ, "node-garage", map[string]any{"name": "Garage"}); err != nil {
				return err
			}
		}
		if t.Equal(now) { // the clients table, so the first poll knows them
			online := map[string]bool{}
			for _, c := range known {
				online[c.MAC] = c.Online
			}
			if err := st.SaveClients(ctx, netw.ID, known, online); err != nil {
				return err
			}
		}
	}
	return nil
}
