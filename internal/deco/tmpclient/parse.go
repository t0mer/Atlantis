package tmpclient

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/t0mer/atlantis/internal/deco"
)

// Parsers for AppV2 results. Field names and enum strings are from the Deco
// app's beans and the @SerializedName values in its dex (docs/tmp-protocol.md,
// "model variants"); they are the same for every Deco model, which differ
// only in which bands and optional fields they send.
//
// Every field is read with deco.Obj, which never fails on a missing or
// oddly typed value, so one unexpected field on some firmware cannot drop a
// node, client or band. Unknown enum strings are kept as raw values.

var errNotObject = errors.New("tmpclient: reply is not a JSON object")

// nodeName is the user's name for a node: custom_nickname (base64) when
// set, else the preset room name (nickname "living_room" -> "Living room").
func nodeName(n deco.Obj) string {
	if c := deco.DecodeName(n.Str("custom_nickname")); c != "" {
		return c
	}
	// nickname is a preset room ID; tolerate firmware that puts a base64
	// name there instead.
	room := strings.ReplaceAll(deco.DecodeName(n.Str("nickname")), "_", " ")
	if room == "" || room == "custom" {
		return n.Str("device_model")
	}
	return deco.TitleFirst(room)
}

// backhaul classifies connection_type the way the app does
// (DeviceInfoItem.java): PLC with a band, wired, PLC, other links, Wi-Fi.
func backhaul(links []string, signal map[string]int) deco.Backhaul {
	b := deco.Backhaul{Links: links}
	if len(signal) > 0 {
		b.Signal = make(map[string]int, len(signal))
		for band, v := range signal {
			b.Signal[deco.NormalizeBand(band)] = v
		}
	}
	var wired, plc, band, other bool
	for _, l := range links {
		switch {
		case l == "wired":
			wired = true
		case l == "plc":
			plc = true
		case strings.HasPrefix(l, "band"):
			band = true
		case l != "":
			other = true
		}
	}
	switch {
	case plc && band:
		b.Type = deco.BackhaulPLCWiFi
	case wired:
		b.Type = deco.BackhaulWired
	case plc:
		b.Type = deco.BackhaulPLC
	case other:
		b.Type = deco.BackhaulOther
	case band:
		b.Type = deco.BackhaulWiFi
	}
	return b
}

// parseDevices maps a DEVICE_LIST_GET result.
func parseDevices(raw json.RawMessage) ([]deco.Device, error) {
	top, ok := deco.ParseObj(raw)
	if !ok {
		return nil, errNotObject
	}
	var out []deco.Device
	for _, n := range top.Objs("device_list") {
		id, mac := n.Str("device_id"), deco.NormalizeMAC(n.Str("mac"))
		if id == "" && mac == "" {
			continue
		}
		role, group, inet, inetErr := n.Str("role"), n.Str("group_status"), n.Str("inet_status"), n.Str("inet_error_msg")
		d := deco.Device{
			ID: id, MAC: mac, IP: n.Str("ip"), Nickname: nodeName(n), Model: n.Str("device_model"),
			HardwareVer: n.Str("hardware_ver"), SoftwareVer: n.Str("software_ver"),
			Master:         strings.EqualFold(role, "master"),
			Online:         !strings.EqualFold(group, "disconnected"),
			InternetOnline: strings.EqualFold(inet, "online") || inetErr == "well",
			ParentID:       n.Str("parent_device_id"),
			Role:           role, GroupStatus: group, InetStatus: inet,
		}
		if !d.InternetOnline && inetErr != "well" {
			d.InternetError = inetErr
		}
		if d.ID == "" {
			d.ID = d.MAC
		}
		if !d.Master {
			d.Backhaul = backhaul(n.Strs("connection_type"), n.Ints("signal_level"))
		}
		out = append(out, d)
	}
	return out, nil
}

// parseClients maps CLIENT_LIST_GET, merging rates from
// CLIENT_LIST_SPEED_GET (kbps, matched by MAC) when speeds is non-nil.
// Clients without a linked node are on the master.
func parseClients(raw, speeds json.RawMessage, masterID string) ([]deco.WifiClient, error) {
	top, ok := deco.ParseObj(raw)
	if !ok {
		return nil, errNotObject
	}
	rates := map[string]deco.Obj{}
	if sp, ok := deco.ParseObj(speeds); ok {
		for _, e := range sp.Objs("client_list_speed") {
			rates[deco.NormalizeMAC(e.Str("mac"))] = e
		}
	}
	var out []deco.WifiClient
	for _, w := range top.Objs("client_list") {
		mac := deco.NormalizeMAC(w.Str("mac"))
		if mac == "" {
			continue
		}
		linked := w.Obj("linked_device_info")
		c := deco.WifiClient{
			MAC: mac, IP: w.Str("ip"), Type: w.Str("client_type"), Online: w.Bool("online"),
			Network: w.Str("interface"), NodeID: linked.Str("device_id"),
			SignalLevel: int(w.Int("signal_level")),
			UpBps:       w.Int("up_speed") * 1000, DownBps: w.Int("down_speed") * 1000,
		}
		if c.Name = deco.DecodeName(w.Str("name")); c.Name == "" {
			c.Name = deco.DecodeName(w.Str("default_name"))
		}
		if c.NodeID == "" {
			c.NodeID = masterID
		}
		for _, l := range linked.Strs("connection_type") {
			switch {
			case l == "wired":
				c.Wired = true
			case strings.HasPrefix(l, "band") && c.Band == "":
				c.Band = deco.NormalizeBand(l)
			}
		}
		if strings.EqualFold(w.Str("wire_type"), "wired") {
			c.Wired = true
		}
		if c.SignalLevel == 0 && c.Band != "" {
			// Live firmware reports per-band levels under linked_device_info.
			for band, v := range linked.Ints("signal_level") {
				if deco.NormalizeBand(band) == c.Band {
					c.SignalLevel = v
				}
			}
		}
		if c.Wired {
			c.Band, c.SignalLevel = "", 0
		}
		if sp, ok := rates[mac]; ok {
			c.UpBps, c.DownBps = sp.Int("up_speed")*1000, sp.Int("down_speed")*1000
		}
		out = append(out, c)
	}
	return out, nil
}

func netConfig(n deco.Obj) deco.NetworkConfig {
	return deco.NetworkConfig{
		Enable: n.Bool("enable"), SSID: deco.DecodeName(n.Str("ssid")),
		Encryption: n.Str("encryption_mode"), Hidden: n.Bool("enable_hide_ssid"),
	}
}

// parseWireless maps WIRELESS_GET: every known band key is a band; within
// it "radio" is the radio, "backhaul" is skipped, and every other object is
// a network (host, guest, iot, ...). Passwords are never read.
func parseWireless(raw json.RawMessage) (*deco.WirelessConfig, error) {
	top, ok := deco.ParseObj(raw)
	if !ok {
		return nil, errNotObject
	}
	out := &deco.WirelessConfig{Bands: map[string]deco.Band{}}
	for name := range top {
		if !deco.IsBand(name) {
			continue
		}
		parts, ok := deco.ParseObj(top[name])
		if !ok {
			continue
		}
		band := deco.Band{Networks: map[string]deco.NetworkConfig{}}
		for key := range parts {
			sub, ok := deco.ParseObj(parts[key])
			if !ok {
				continue
			}
			switch key {
			case "radio":
				band.Radio = deco.Radio{Enable: sub.Bool("enable"), Channel: sub.Str("channel")}
			case "backhaul":
			default:
				band.Networks[key] = netConfig(sub)
			}
		}
		out.Bands[name] = band
	}
	return out, nil
}

// parseMLO maps MLO_NETWORK_GET (Wi-Fi 7 multi-link) into a pseudo-band
// "mlo" with a single "mlo" network; nil when MLO is absent.
func parseMLO(raw json.RawMessage) *deco.Band {
	top, _ := deco.ParseObj(raw)
	m := top.Obj("mlo")
	if len(m) == 0 {
		return nil
	}
	return &deco.Band{
		Radio:    deco.Radio{Enable: m.Bool("enable"), Channel: strings.Join(m.Strs("band"), "+")},
		Networks: map[string]deco.NetworkConfig{"mlo": netConfig(m)},
	}
}

// parseFirmware maps FW_LATEST_GET.
func parseFirmware(raw json.RawMessage) ([]deco.Firmware, error) {
	top, ok := deco.ParseObj(raw)
	if !ok {
		return nil, errNotObject
	}
	var out []deco.Firmware
	for _, f := range top.Objs("fw_list") {
		out = append(out, deco.Firmware{
			Model: f.Str("device_model"), NodeIDs: f.Strs("device_id_list"),
			LatestVersion: deco.DecodeName(f.Str("version")), ReleaseDate: f.Str("release_date"),
			ReleaseNote: decodeNote(f.Str("release_note")), UpgradeAvailable: f.Bool("need_to_upgrade"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// decodeNote decodes a base64 release note, which may contain newlines
// that DecodeName rejects as non-printable.
func decodeNote(s string) string {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) > 0 && utf8.Valid(b) {
		return string(b)
	}
	return s
}

// parseWAN maps IPV4_GET. Online comes from the caller (the master node's
// inet status, as the app shows it). user_info (PPPoE credentials) is never
// read.
func parseWAN(raw json.RawMessage, online bool) (*deco.WANStatus, error) {
	top, ok := deco.ParseObj(raw)
	if !ok {
		return nil, errNotObject
	}
	wan := top.Obj("wan")
	w, lan := wan.Obj("ip_info"), top.Obj("lan").Obj("ip_info")
	return &deco.WANStatus{
		Online: online, MAC: deco.NormalizeMAC(w.Str("mac")), IP: w.Str("ip"), Gateway: w.Str("gateway"),
		Mask: w.Str("mask"), DNS1: w.Str("dns1"), DNS2: w.Str("dns2"), DialType: wan.Str("dial_type"),
		LANIP: lan.Str("ip"), LANMask: lan.Str("mask"),
	}, nil
}

// parsePerformance maps DEVICE_RESOURCE_INFO_GET: cpu_usage in percent,
// memory_usage of memory_total. The values may sit at the top level or in a
// device_list entry.
func parsePerformance(raw json.RawMessage) (*deco.Performance, error) {
	top, ok := deco.ParseObj(raw)
	if !ok {
		return nil, errNotObject
	}
	r := top
	if !r.Has("cpu_usage") && !r.Has("memory_total") {
		for _, e := range top.Objs("device_list") {
			if e.Has("cpu_usage") || e.Has("memory_total") {
				r = e
				break
			}
		}
	}
	p := &deco.Performance{CPUUsage: r.Float("cpu_usage") / 100}
	if total := r.Float("memory_total"); total > 0 {
		p.MemUsage = r.Float("memory_usage") / total
	}
	return p, nil
}

// components returns the component IDs from a COMP_NEGOTIATE result.
func components(neg json.RawMessage) map[string]bool {
	top, _ := deco.ParseObj(neg)
	out := map[string]bool{}
	for _, c := range top.Objs("component_list") {
		if id := c.Str("id"); id != "" {
			out[id] = true
		}
	}
	return out
}
