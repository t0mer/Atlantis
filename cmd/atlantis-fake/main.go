// Command atlantis-fake runs a pretend Deco mesh for UI development and the
// end-to-end tests: an SSH server speaking the Deco app's TMP protocol with
// three nodes and a changing set of clients. It never talks to real
// hardware and is not part of releases.
//
//	go run ./cmd/atlantis-fake            # prints the env to point serve at it
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn/fakessh"
)

const (
	email    = "demo@example.com"
	password = "demo-password"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

type client struct {
	mac, ip, name, typ, network, node, band string
	wired                                   bool
	signal                                  int
	online                                  bool
	down, up                                int // kbps when busy
}

// The mesh: a master in the living room and two satellites.
var nodes = []map[string]any{
	{"device_id": "node-main", "mac": "00-00-5E-00-53-F1", "ip": "192.0.2.1", "role": "master", "device_model": "X50",
		"nickname": "living_room", "hardware_ver": "1.0", "software_ver": "1.2.3 Build 20260801", "group_status": "connected",
		"inet_status": "online", "inet_error_msg": "well", "connection_type": []string{}},
	{"device_id": "node-office", "mac": "00-00-5E-00-53-F2", "ip": "192.0.2.2", "role": "slave", "device_model": "X50",
		"nickname": "custom", "custom_nickname": b64("משרד"), "hardware_ver": "1.0", "software_ver": "1.2.3 Build 20260801",
		"group_status": "connected", "inet_status": "online", "parent_device_id": "node-main",
		"connection_type": []string{"band6"}, "signal_level": map[string]any{"band2_4": "2", "band5": "3", "band6": "4"}},
	{"device_id": "node-garage", "mac": "00-00-5E-00-53-F3", "ip": "192.0.2.3", "role": "slave", "device_model": "M5",
		"nickname": "garage", "hardware_ver": "2.0", "software_ver": "1.9.1 Build 20250909",
		"group_status": "connected", "inet_status": "online", "parent_device_id": "node-main", "connection_type": []string{"wired"}},
}

var clients = []*client{
	{"00:00:5e:00:53:01", "192.0.2.21", "Dana's iPhone", "phone", "main", "node-main", "band5", false, 3, true, 4200, 380},
	{"00:00:5e:00:53:02", "192.0.2.22", "MacBook Pro", "laptop", "main", "node-office", "band6", false, 4, true, 18500, 2100},
	{"00:00:5e:00:53:03", "192.0.2.23", "הטלפון של נועה", "phone", "main", "node-main", "band5", false, 2, true, 900, 120},
	{"00:00:5e:00:53:04", "192.0.2.24", "Living room TV", "entertainment", "main", "node-main", "", true, 0, true, 12000, 300},
	{"00:00:5e:00:53:05", "192.0.2.25", "iPad", "tablet", "main", "node-office", "band5", false, 3, false, 2500, 200},
	{"00:00:5e:00:53:06", "192.0.2.26", "מדפסת", "printer", "main", "node-office", "band2_4", false, 2, true, 0, 0},
	{"00:00:5e:00:53:07", "192.0.2.27", "Thermostat", "iot_device", "iot", "node-garage", "band2_4", false, 2, true, 3, 1},
	{"00:00:5e:00:53:08", "192.0.2.28", "Doorbell camera", "iot_device", "iot", "node-garage", "band2_4", false, 1, true, 40, 1800},
	{"00:00:5e:00:53:09", "192.0.2.29", "Robot vacuum", "iot_device", "iot", "node-garage", "band2_4", false, 2, false, 5, 2},
	{"00:00:5e:00:53:0a", "192.0.2.30", "Guest phone", "phone", "guest", "node-main", "band5", false, 3, true, 1300, 90},
	{"0a:11:22:33:44:55", "192.0.2.31", "", "phone", "main", "node-office", "band5", false, 2, true, 600, 60}, // randomised MAC
	{"00:00:5e:00:53:0c", "192.0.2.32", "NAS", "pc", "main", "node-main", "", true, 0, true, 300, 9500},
	{"00:00:5e:00:53:0d", "192.0.2.33", "Kids' laptop", "laptop", "main", "node-office", "band6", false, 4, false, 7000, 500},
	{"00:00:5e:00:53:0e", "192.0.2.34", "Smart plug", "iot_device", "iot", "node-garage", "band2_4", false, 1, true, 1, 1},
}

var mu sync.Mutex

func publish(d *fakedeco.Device) {
	mu.Lock()
	defer mu.Unlock()
	var list, speeds []any
	for _, c := range clients {
		conn := []string{c.band}
		if c.wired {
			conn = []string{"wired"}
		}
		wire := "wireless"
		if c.wired {
			wire = "wired"
		}
		list = append(list, map[string]any{
			"mac": c.mac, "ip": c.ip, "name": b64(c.name), "client_type": c.typ, "online": c.online,
			"interface": c.network, "wire_type": wire,
			"linked_device_info": map[string]any{"device_id": c.node, "connection_type": conn, "signal_level": map[string]any{c.band: c.signal}},
		})
		up, down := 0, 0
		if c.online {
			jit := 0.5 + rand.Float64()
			up, down = int(float64(c.up)*jit), int(float64(c.down)*jit)
		}
		speeds = append(speeds, map[string]any{"mac": c.mac, "up_speed": up, "down_speed": down})
	}
	d.SetResult(tmp.OpClientListGet, map[string]any{"client_list": list})
	d.SetResult(tmp.OpClientListSpeedGet, map[string]any{"client_list_speed": speeds})
}

func main() {
	addr := flag.String("listen", "127.0.0.1:0", "address for the fake Deco's SSH service")
	churn := flag.Duration("churn", 40*time.Second, "how often a client comes or goes (0 = never)")
	flag.Parse()

	srv, err := fakessh.NewAt(*addr, sshconn.Username(email, false, false), password)
	if err != nil {
		log.Fatal(err)
	}
	d := srv.Device
	d.SetResult(tmp.OpDeviceListGet, map[string]any{"device_list": nodes})
	host := func(on bool, ssid string) map[string]any {
		return map[string]any{"enable": on, "ssid": b64(ssid), "password": b64("not-shown"), "encryption_mode": "wpa2"}
	}
	d.SetResult(tmp.OpWirelessGet, map[string]any{
		"band2_4": map[string]any{"host": host(true, "Atlantis"), "guest": host(true, "Atlantis Guest"), "iot": host(true, "Atlantis IoT"), "radio": map[string]any{"enable": true, "channel": "auto"}},
		"band5_1": map[string]any{"host": host(true, "Atlantis"), "guest": host(true, "Atlantis Guest"), "iot": host(false, "Atlantis IoT"), "radio": map[string]any{"enable": true, "channel": "36"}},
		"band6":   map[string]any{"host": host(true, "Atlantis"), "guest": host(false, "Atlantis Guest"), "radio": map[string]any{"enable": true, "channel": "auto"}},
	})
	d.SetResult(tmp.OpFWLatestGet, map[string]any{"fw_list": []any{
		map[string]any{"device_model": "M5", "version": "1.9.5 Build 20260602", "need_to_upgrade": true, "device_id_list": []string{"node-garage"},
			"release_date": "2026-06-02", "release_note": b64("Stability and security fixes.")},
	}})
	publish(d)

	h, p := srv.Addr()
	fmt.Printf("fake Deco SSH on %s:%d\n\nexport ATLANTIS_DECO_HOST=%s ATLANTIS_DECO_PORT=%d ATLANTIS_DECO_EMAIL=%s ATLANTIS_DECO_PASSWORD=%s\n",
		h, p, h, p, email, password)

	if *churn > 0 {
		go func() {
			for range time.Tick(*churn) {
				mu.Lock()
				c := clients[rand.IntN(len(clients))]
				c.online = !c.online
				mu.Unlock()
				publish(d)
			}
		}()
	}
	go func() { // fresh rates every few seconds
		for range time.Tick(5 * time.Second) {
			publish(d)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Close()
}
