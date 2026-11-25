package tmpclient

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco"
)

// Fixtures in this file are SYNTHETIC: built from the field names and enum
// strings in the Deco app (docs/tmp-protocol.md, "model variants") and the
// shape of a live Deco M5 reply. They stand in for model families we have
// not recorded yet; recorded fixtures replace them as they arrive.

func b64s(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func nodeByID(t *testing.T, ds []deco.Device, id string) deco.Device {
	t.Helper()
	for _, d := range ds {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("node %s missing from %+v", id, ds)
	return deco.Device{}
}

// One mesh per family: M5-style dual band, X50-style 6 GHz, BE-style MLO,
// P-series powerline, wired backhaul and a 4G CPE main unit.
const deviceListAllFamilies = `{"device_list":[
 {"device_id":"m1","mac":"AA-BB-CC-00-00-01","ip":"192.0.2.1","role":"master","device_model":"X20-4G",
  "nickname":"living_room","custom_nickname":"","hardware_ver":"1.0","software_ver":"1.1.0",
  "group_status":"connected","inet_status":"offline","inet_error_msg":"sim_absent","connection_type":[]},
 {"device_id":"s6","mac":"aabbcc000002","role":"slave","device_model":"X50","custom_nickname":"` + "T2ZmaWNl" + `",
  "group_status":"connected","inet_status":"online","inet_error_msg":"well","parent_device_id":"m1",
  "connection_type":["band6"],"signal_level":{"band2_4":"2","band5":"3","band6":4}},
 {"device_id":"smlo","mac":"AA:BB:CC:00:00:03","role":"slave","device_model":"BE85","nickname":"kitchen",
  "group_status":"connected","inet_status":"online","parent_device_id":"m1",
  "connection_type":["band2+band5+band6"],"signal_level":{"band5_1":"3"}},
 {"device_id":"splc","mac":"AA-BB-CC-00-00-04","role":"slave","device_model":"P9","nickname":"custom","custom_nickname":"` + "15fXk9eoINep15nXoNeU" + `",
  "group_status":"connected","inet_status":"online","parent_device_id":"m1","connection_type":["plc","band5"],"support_plc":true},
 {"device_id":"spl","mac":"AA-BB-CC-00-00-05","role":"slave","device_model":"P9","nickname":"office",
  "group_status":"disconnected","inet_status":"offline","parent_device_id":"m1","connection_type":"plc"},
 {"device_id":"sw","mac":"AA-BB-CC-00-00-06","role":"slave","device_model":"M5","nickname":"garage",
  "group_status":"connected","inet_status":"online","parent_device_id":"m1","connection_type":["wired"]},
 {"device_id":"sz","mac":"AA-BB-CC-00-00-07","role":"slave","device_model":"X50","nickname":"attic",
  "group_status":"connected","inet_status":"online","connection_type":["zigbee"],"some_future_field":{"x":1}},
 "not an object",
 {"device_id":42}
]}`

func TestParseDevicesAcrossFamilies(t *testing.T) {
	ds, err := parseDevices(json.RawMessage(deviceListAllFamilies))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 8 {
		t.Fatalf("want 8 nodes (a non-object skipped, a numeric id kept), got %d", len(ds))
	}
	nodeByID(t, ds, "42")
	m := nodeByID(t, ds, "m1")
	if !m.Master || m.InternetOnline || m.InternetError != "sim_absent" || m.Nickname != "Living room" || m.Backhaul.Type != "" || m.MAC != "aa:bb:cc:00:00:01" {
		t.Errorf("CPE master: %+v", m)
	}
	s6 := nodeByID(t, ds, "s6")
	if s6.Backhaul.Type != deco.BackhaulWiFi || s6.Backhaul.Signal["band6"] != 4 || s6.Backhaul.Signal["band5_1"] != 3 || s6.Nickname != "Office" || !s6.InternetOnline || s6.InternetError != "" {
		t.Errorf("6 GHz satellite: %+v", s6)
	}
	if b := nodeByID(t, ds, "smlo").Backhaul; b.Type != deco.BackhaulWiFi || b.Links[0] != "band2+band5+band6" {
		t.Errorf("MLO satellite: %+v", b)
	}
	if p := nodeByID(t, ds, "splc"); p.Backhaul.Type != deco.BackhaulPLCWiFi || p.Nickname != "חדר שינה" {
		t.Errorf("PLC+Wi-Fi satellite: %+v", p)
	}
	if p := nodeByID(t, ds, "spl"); p.Backhaul.Type != deco.BackhaulPLC || p.Online {
		t.Errorf("PLC-only, disconnected satellite: %+v", p)
	}
	if nodeByID(t, ds, "sw").Backhaul.Type != deco.BackhaulWired {
		t.Error("wired backhaul")
	}
	if nodeByID(t, ds, "sz").Backhaul.Type != deco.BackhaulOther {
		t.Error("unknown link type must map to other, not fail")
	}
}

func TestParseClients(t *testing.T) {
	list := `{"client_list":[
	  {"mac":"11-22-33-44-55-01","ip":"192.0.2.10","name":"` + b64s("Tomer's iPhone") + `","client_type":"phone","online":true,
	   "interface":"main","signal_level":3,"linked_device_info":{"device_id":"s6","connection_type":["band6"]}},
	  {"mac":"11-22-33-44-55-02","name":"` + b64s("TV") + `","online":"1","interface":"iot",
	   "linked_device_info":{"device_id":"sw","connection_type":["wired"]},"signal_level":2},
	  {"mac":"11-22-33-44-55-03","name":"","default_name":"` + b64s("android-abc") + `","online":false,"interface":"guest",
	   "linked_device_info":{"connection_type":["band5"]}},
	  {"mac":"11-22-33-44-55-04","online":true,"interface":"mlo","linked_device_info":{"device_id":"smlo","connection_type":["band2+band5"]}},
	  {"mac":""},
	  12
	]}`
	speeds := `{"client_list_speed":[{"mac":"11:22:33:44:55:01","up_speed":"120","down_speed":2500}]}`
	cs, err := parseClients(json.RawMessage(list), json.RawMessage(speeds), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 4 {
		t.Fatalf("want 4 clients, got %d: %+v", len(cs), cs)
	}
	p := cs[0]
	if p.Name != "Tomer's iPhone" || p.Band != "band6" || p.NodeID != "s6" || p.Wired || p.UpBps != 120000 || p.DownBps != 2500000 || p.Type != "phone" || p.SignalLevel != 3 {
		t.Errorf("phone: %+v", p)
	}
	if tv := cs[1]; !tv.Wired || tv.Band != "" || tv.SignalLevel != 0 || tv.Network != deco.NetworkIoT || !tv.Online {
		t.Errorf("wired iot client: %+v", tv)
	}
	if g := cs[2]; g.Name != "android-abc" || g.NodeID != "m1" || g.Band != "band5_1" || g.Online {
		t.Errorf("guest client on master: %+v", g)
	}
	if mlo := cs[3]; mlo.Network != deco.NetworkMLO || mlo.Band != "band2+band5" {
		t.Errorf("MLO client: %+v", mlo)
	}
}

const wirelessAllBands = `{
 "band2_4":{"host":{"enable":true,"ssid":"SG9tZQ==","password":"c2VjcmV0LTI0","encryption_mode":"wpa2"},
            "guest":{"enable":false,"ssid":"R3Vlc3Q=","password":"c2VjcmV0LWc="},
            "iot":{"enable":"1","ssid":"SW9U","password":"c2VjcmV0LWlvdA==","enable_wpa3":false},
            "radio":{"enable":true,"channel":"auto"},"backhaul":{"channel":6}},
 "band5_1":{"host":{"enable":true,"ssid":"SG9tZQ==","enable_hide_ssid":true},"radio":{"enable":true,"channel":36}},
 "band5_2":{"host":{"enable":true,"ssid":"SG9tZQ=="}},
 "band6":{"host":{"enable":true,"ssid":"SG9tZQ=="},"radio":{"enable":false,"channel":"5"}},
 "band60":{"host":{"enable":false,"ssid":"SG9tZQ=="}},
 "config_spec":{"ssid":"SG9tZQ==","password":"c2VjcmV0LXNwZWM="},
 "band6_2":"unexpected"
}`

func TestParseWirelessAllBands(t *testing.T) {
	w, err := parseWireless(json.RawMessage(wirelessAllBands))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"band2_4", "band5_1", "band5_2", "band6", "band60"} {
		if w.Bands[b].Networks["host"].SSID != "Home" {
			t.Errorf("%s host: %+v", b, w.Bands[b])
		}
	}
	if _, ok := w.Bands["config_spec"]; ok {
		t.Error("config_spec is not a band")
	}
	b24 := w.Bands["band2_4"]
	if !b24.Networks["iot"].Enable || b24.Networks["iot"].SSID != "IoT" || b24.Networks["guest"].Enable || b24.Radio.Channel != "auto" {
		t.Errorf("band2_4: %+v", b24)
	}
	if _, ok := b24.Networks["backhaul"]; ok {
		t.Error("backhaul is not a network")
	}
	if w.Bands["band5_1"].Radio.Channel != "36" || !w.Bands["band5_1"].Networks["host"].Hidden {
		t.Errorf("band5_1: %+v", w.Bands["band5_1"])
	}
	// Passwords must never reach the model, in any form.
	dump := fmt.Sprintf("%+v", w)
	for _, secret := range []string{"c2VjcmV0", "secret"} {
		if strings.Contains(dump, secret) {
			t.Fatalf("password leaked into the model: %s", dump)
		}
	}
}

func TestParseMLO(t *testing.T) {
	b := parseMLO(json.RawMessage(`{"mlo":{"enable":true,"band":["band2_4","band5_1","band6"],"ssid":"` + b64s("Home-MLO") + `","password":"x","encryption_mode":"wpa3"}}`))
	if b == nil || b.Networks["mlo"].SSID != "Home-MLO" || b.Radio.Channel != "band2_4+band5_1+band6" {
		t.Fatalf("%+v", b)
	}
	if parseMLO(json.RawMessage(`{}`)) != nil {
		t.Fatal("no mlo object means no MLO band")
	}
}

func TestParseFirmware(t *testing.T) {
	fw, err := parseFirmware(json.RawMessage(`{"fw_list":[
	  {"device_model":"X50","version":"1.3.0 Build 20250101","release_date":"2025-01-01","release_note":"` + b64s("Fixes\nmore fixes") + `","need_to_upgrade":true,"device_id_list":["s6","sz"]},
	  {"device_model":"M5","version":"1.6.0","need_to_upgrade":"0"}]}`))
	if err != nil || len(fw) != 2 {
		t.Fatalf("%+v %v", fw, err)
	}
	if fw[1].Model != "X50" || !fw[1].UpgradeAvailable || fw[1].ReleaseNote != "Fixes\nmore fixes" || len(fw[1].NodeIDs) != 2 || fw[0].UpgradeAvailable {
		t.Fatalf("%+v", fw)
	}
}

func TestParseWANAndPerformance(t *testing.T) {
	w, err := parseWAN(json.RawMessage(`{"wan":{"ip_info":{"ip":"203.0.113.5","mask":"255.255.255.0","gateway":"203.0.113.1","dns1":"1.1.1.1"},"dial_type":"pppoe","user_info":{"username":"dXNlcg==","password":"cHc="}},"lan":{"ip_info":{"ip":"192.168.68.1","mask":"255.255.252.0"}}}`), true)
	if err != nil || w.IP != "203.0.113.5" || w.DialType != "pppoe" || w.LANIP != "192.168.68.1" || !w.Online {
		t.Fatalf("%+v %v", w, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", w), "cHc=") {
		t.Fatal("PPPoE password leaked")
	}
	for _, raw := range []string{
		`{"cpu_usage":25,"memory_usage":128,"memory_total":512}`,
		`{"device_list":[{"device_id":"m1","cpu_usage":"25","memory_usage":"128","memory_total":"512"}]}`,
	} {
		p, err := parsePerformance(json.RawMessage(raw))
		if err != nil || p.CPUUsage != 0.25 || p.MemUsage != 0.25 {
			t.Errorf("%s -> %+v %v", raw, p, err)
		}
	}
}

func TestComponents(t *testing.T) {
	c := components(json.RawMessage(`{"component_list":[{"id":"wan","ver_code":1},{"id":"device_resource","ver_code":2}]}`))
	if !c["wan"] || !c["device_resource"] || c["dsl_wan"] {
		t.Fatalf("%v", c)
	}
}

// Shape seen live on a Deco M5 (values replaced): the level is per band
// under linked_device_info, and wire_type is set.
func TestParseClientLiveShape(t *testing.T) {
	list := `{"client_list":[{"mac":"AA-BB-CC-DD-EE-10","ip":"192.0.2.20","name":"` + b64s("Tablet") + `","client_type":"tablet",
	  "online":true,"interface":"main","wire_type":"wireless","access_host":"1","client_mesh":true,
	  "linked_device_info":{"connection_type":["band2_4"],"device_id":"m1","signal_level":{"band2_4":2,"band5":0}}}]}`
	cs, err := parseClients(json.RawMessage(list), nil, "m1")
	if err != nil || len(cs) != 1 {
		t.Fatalf("%+v %v", cs, err)
	}
	if c := cs[0]; c.SignalLevel != 2 || c.Band != "band2_4" || c.Wired || c.NodeID != "m1" || c.Name != "Tablet" {
		t.Fatalf("%+v", c)
	}
	wired, _ := parseClients(json.RawMessage(`{"client_list":[{"mac":"AA-BB-CC-DD-EE-11","wire_type":"wired","linked_device_info":{"connection_type":[]}}]}`), nil, "m1")
	if !wired[0].Wired {
		t.Fatal("wire_type wired")
	}
}

// Review findings: odd values must not drop an entry, and other edge cases.
func TestOddValuesKeepEntries(t *testing.T) {
	ds, _ := parseDevices(json.RawMessage(`{"device_list":[
	  {"device_id":"a","role":"slave","signal_level":{"band2_4":"n/a"},"group_status":1,"connection_type":["band5",7]},
	  {"device_id":"b","role":"slave","nickname":"` + b64s("סלון") + `","signal_level":"weird","connection_type":{"x":1}}]}`))
	if len(ds) != 2 {
		t.Fatalf("nodes dropped: %+v", ds)
	}
	// A numeric link is kept as text and counts as an unknown link type.
	if ds[0].GroupStatus != "1" || ds[0].Backhaul.Signal["band2_4"] != 0 || ds[0].Backhaul.Type != deco.BackhaulOther || ds[0].Backhaul.Links[1] != "7" {
		t.Errorf("node a: %+v", ds[0])
	}
	if ds[1].Nickname != "סלון" {
		t.Errorf("non-ASCII room name corrupted: %q", ds[1].Nickname)
	}
	cs, _ := parseClients(json.RawMessage(`{"client_list":[
	  {"mac":"AA-BB-CC-DD-EE-01","online":"unknown","up_speed":"N/A","linked_device_info":"x"},
	  {"mac":"AA-BB-CC-DD-EE-02","online":true,"down_speed":[1]}]}`), nil, "m")
	if len(cs) != 2 || cs[0].Online || cs[0].UpBps != 0 || cs[0].NodeID != "m" {
		t.Fatalf("clients dropped or wrong: %+v", cs)
	}
	fw, _ := parseFirmware(json.RawMessage(`{"fw_list":[{"device_model":"M5","device_id_list":"dev-1"}]}`))
	if len(fw) != 1 || len(fw[0].NodeIDs) != 1 {
		t.Fatalf("firmware entry with a single id dropped: %+v", fw)
	}
	p, err := parsePerformance(json.RawMessage(`{"device_list":[{"cpu_usage":"n/a"},{"cpu_usage":40,"memory_usage":1,"memory_total":4}]}`))
	if err != nil || p.CPUUsage != 0 && p.CPUUsage != 0.4 {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := parsePerformance(json.RawMessage(`{"device_list":"x","cpu_usage":1}`)); err != nil || p.CPUUsage != 0.01 {
		t.Fatalf("1%% CPU must stay 1%%: %+v %v", p, err)
	}
	w, _ := parseWireless(json.RawMessage(`{"band_steering":{"host":{"ssid":"eA=="}},"band2_4":{"host":{"ssid":"eA=="},"radio":"x"}}`))
	if _, ok := w.Bands["band_steering"]; ok || w.Bands["band2_4"].Networks["host"].SSID != "x" {
		t.Fatalf("%+v", w)
	}
}

// Recorded from a Deco M5 (master, firmware 1.9.x) over SSH and sanitised:
// names, MACs and IPs are placeholders. These are real replies, unlike the
// synthetic fixtures above.
func TestRecordedM5Fixtures(t *testing.T) {
	read := func(name string) json.RawMessage {
		b, err := os.ReadFile("testdata/m5/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ds, err := parseDevices(read("device_list_get.json"))
	if err != nil || len(ds) == 0 {
		t.Fatalf("devices: %v %v", ds, err)
	}
	m := ds[0]
	if !m.Master || !m.Online || !m.InternetOnline || m.Model == "" || m.MAC == "" || m.Nickname == "" {
		t.Fatalf("M5 master: %+v", m)
	}
	w, err := parseWireless(read("wireless_get.json"))
	if err != nil || len(w.Bands) != 2 {
		t.Fatalf("wireless: %+v %v", w, err)
	}
	for _, band := range []string{"band2_4", "band5_1"} {
		b := w.Bands[band]
		for _, net := range []string{"host", "guest", "iot"} {
			if _, ok := b.Networks[net]; !ok {
				t.Errorf("%s/%s missing", band, net)
			}
		}
		if !b.Networks["host"].Enable || b.Networks["host"].SSID == "" {
			t.Errorf("%s host: %+v", band, b.Networks["host"])
		}
	}
	fw, err := parseFirmware(read("fw_latest_get.json"))
	if err != nil || len(fw) == 0 || fw[0].Model == "" || fw[0].LatestVersion == "" {
		t.Fatalf("firmware: %+v %v", fw, err)
	}
	if _, err := parseClients(read("client_list_get.json"), read("client_list_speed_get.json"), m.ID); err != nil {
		t.Fatal(err)
	}
}
