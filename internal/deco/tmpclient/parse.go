package tmpclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/t0mer/atlantis/internal/deco"
)

// Parsers for AppV2 results. Field names and enum strings are from the Deco
// app's beans and the @SerializedName values in its dex (docs/tmp-protocol.md,
// "model variants"); they are the same for every Deco model, which differ
// only in which bands and optional fields they send. Each parser keeps going
// past entries it cannot read and keeps unknown enum strings as raw values.

// strList decodes a list of strings or a single string.
type strList []string

func (s *strList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = nil
		return nil
	}
	if b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		if one == "" {
			*s = nil
		} else {
			*s = strList{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// flexStr decodes a string or a number as text.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexStr(s)
		return nil
	}
	*f = flexStr(string(b))
	return nil
}

// eachEntry decodes every element of a JSON list into a T, skipping
// elements that do not decode. It returns how many were skipped.
func eachEntry[T any](list json.RawMessage, fn func(T)) (skipped int) {
	var items []json.RawMessage
	if json.Unmarshal(list, &items) != nil {
		return 0
	}
	for _, it := range items {
		var v T
		if json.Unmarshal(it, &v) != nil {
			skipped++
			continue
		}
		fn(v)
	}
	return skipped
}

type wireNode struct {
	DeviceID       string                  `json:"device_id"`
	MAC            string                  `json:"mac"`
	IP             string                  `json:"ip"`
	Nickname       string                  `json:"nickname"`
	CustomNickname string                  `json:"custom_nickname"`
	Model          string                  `json:"device_model"`
	HardwareVer    string                  `json:"hardware_ver"`
	SoftwareVer    string                  `json:"software_ver"`
	Role           string                  `json:"role"`
	GroupStatus    string                  `json:"group_status"`
	InetStatus     string                  `json:"inet_status"`
	InetErrorMsg   string                  `json:"inet_error_msg"`
	ParentID       string                  `json:"parent_device_id"`
	ConnectionType strList                 `json:"connection_type"`
	SignalLevel    map[string]deco.FlexInt `json:"signal_level"`
	ResourceSupp   deco.FlexBool           `json:"resource_support"`
}

// nodeName is the user's name for a node: custom_nickname (base64) when
// set, else the preset room name (nickname "living_room" -> "Living room").
func nodeName(n wireNode) string {
	if c := deco.DecodeName(n.CustomNickname); c != "" {
		return c
	}
	// nickname is a preset room ID; tolerate firmware that puts a base64
	// name there instead.
	room := strings.ReplaceAll(deco.DecodeName(n.Nickname), "_", " ")
	if room == "" || room == "custom" {
		return n.Model
	}
	return strings.ToUpper(room[:1]) + room[1:]
}

// backhaul classifies connection_type the way the app does
// (DeviceInfoItem.java): PLC with a band, wired, PLC, other links, Wi-Fi.
func backhaul(n wireNode) deco.Backhaul {
	b := deco.Backhaul{Links: []string(n.ConnectionType)}
	if len(n.SignalLevel) > 0 {
		b.Signal = make(map[string]int, len(n.SignalLevel))
		for band, v := range n.SignalLevel {
			b.Signal[deco.NormalizeBand(band)] = int(v)
		}
	}
	var wired, plc, band, other bool
	for _, l := range n.ConnectionType {
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
	var r struct {
		DeviceList json.RawMessage `json:"device_list"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("device list: %w", err)
	}
	var out []deco.Device
	eachEntry(r.DeviceList, func(n wireNode) {
		if n.DeviceID == "" && n.MAC == "" {
			return
		}
		master := strings.EqualFold(n.Role, "master")
		d := deco.Device{
			ID: n.DeviceID, MAC: deco.NormalizeMAC(n.MAC), IP: n.IP,
			Nickname: nodeName(n), Model: n.Model,
			HardwareVer: n.HardwareVer, SoftwareVer: n.SoftwareVer,
			Master:         master,
			Online:         !strings.EqualFold(n.GroupStatus, "disconnected"),
			InternetOnline: strings.EqualFold(n.InetStatus, "online") || n.InetErrorMsg == "well",
			ParentID:       n.ParentID,
			Role:           n.Role, GroupStatus: n.GroupStatus, InetStatus: n.InetStatus,
		}
		if !d.InternetOnline && n.InetErrorMsg != "well" {
			d.InternetError = n.InetErrorMsg
		}
		if d.ID == "" {
			d.ID = d.MAC
		}
		if !master {
			d.Backhaul = backhaul(n)
		}
		out = append(out, d)
	})
	return out, nil
}

type wireClient struct {
	MAC         string        `json:"mac"`
	IP          string        `json:"ip"`
	Name        string        `json:"name"`
	DefaultName string        `json:"default_name"`
	ClientType  string        `json:"client_type"`
	Online      deco.FlexBool `json:"online"`
	Interface   string        `json:"interface"`
	Linked      struct {
		DeviceID       string  `json:"device_id"`
		ConnectionType strList `json:"connection_type"`
	} `json:"linked_device_info"`
	SignalLevel deco.FlexInt `json:"signal_level"`
	UpSpeed     deco.FlexInt `json:"up_speed"`
	DownSpeed   deco.FlexInt `json:"down_speed"`
}

type wireSpeed struct {
	MAC       string       `json:"mac"`
	UpSpeed   deco.FlexInt `json:"up_speed"`
	DownSpeed deco.FlexInt `json:"down_speed"`
}

// parseClients maps CLIENT_LIST_GET, merging rates from
// CLIENT_LIST_SPEED_GET (kbps, matched by MAC) when speeds is non-nil.
// Clients without a linked node are on the master.
func parseClients(raw, speeds json.RawMessage, masterID string) ([]deco.WifiClient, error) {
	var r struct {
		ClientList json.RawMessage `json:"client_list"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("client list: %w", err)
	}
	rates := map[string]wireSpeed{}
	if speeds != nil {
		var s struct {
			List json.RawMessage `json:"client_list_speed"`
		}
		if json.Unmarshal(speeds, &s) == nil {
			eachEntry(s.List, func(w wireSpeed) { rates[deco.NormalizeMAC(w.MAC)] = w })
		}
	}
	var out []deco.WifiClient
	eachEntry(r.ClientList, func(w wireClient) {
		mac := deco.NormalizeMAC(w.MAC)
		if mac == "" {
			return
		}
		c := deco.WifiClient{
			MAC: mac, IP: w.IP, Type: w.ClientType, Online: bool(w.Online),
			Network: w.Interface, NodeID: w.Linked.DeviceID, SignalLevel: int(w.SignalLevel),
			UpBps: int64(w.UpSpeed) * 1000, DownBps: int64(w.DownSpeed) * 1000,
		}
		if c.Name = deco.DecodeName(w.Name); c.Name == "" {
			c.Name = deco.DecodeName(w.DefaultName)
		}
		if c.NodeID == "" {
			c.NodeID = masterID
		}
		for _, l := range w.Linked.ConnectionType {
			switch {
			case l == "wired":
				c.Wired = true
			case strings.HasPrefix(l, "band") && c.Band == "":
				c.Band = deco.NormalizeBand(l)
			}
		}
		if c.Wired {
			c.Band, c.SignalLevel = "", 0
		}
		if sp, ok := rates[mac]; ok {
			c.UpBps, c.DownBps = int64(sp.UpSpeed)*1000, int64(sp.DownSpeed)*1000
		}
		out = append(out, c)
	})
	return out, nil
}

type wireNet struct {
	Enable         deco.FlexBool `json:"enable"`
	SSID           string        `json:"ssid"`
	EncryptionMode string        `json:"encryption_mode"`
	HideSSID       deco.FlexBool `json:"enable_hide_ssid"`
}

// parseWireless maps WIRELESS_GET: every "band*" key is a band; within it
// "radio" is the radio, "backhaul" is skipped, and every other object is a
// network (host, guest, iot, ...). Passwords are never read.
func parseWireless(raw json.RawMessage) (*deco.WirelessConfig, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("wireless: %w", err)
	}
	out := &deco.WirelessConfig{Bands: map[string]deco.Band{}}
	for name, braw := range top {
		if !strings.HasPrefix(name, "band") {
			continue
		}
		var parts map[string]json.RawMessage
		if json.Unmarshal(braw, &parts) != nil {
			continue
		}
		band := deco.Band{Networks: map[string]deco.NetworkConfig{}}
		for key, praw := range parts {
			switch key {
			case "radio":
				var r struct {
					Enable  deco.FlexBool `json:"enable"`
					Channel flexStr       `json:"channel"`
				}
				if json.Unmarshal(praw, &r) == nil {
					band.Radio = deco.Radio{Enable: bool(r.Enable), Channel: string(r.Channel)}
				}
			case "backhaul":
			default:
				var n wireNet
				if praw[0] != '{' || json.Unmarshal(praw, &n) != nil {
					continue
				}
				band.Networks[key] = deco.NetworkConfig{
					Enable: bool(n.Enable), SSID: deco.DecodeName(n.SSID),
					Encryption: n.EncryptionMode, Hidden: bool(n.HideSSID),
				}
			}
		}
		out.Bands[name] = band
	}
	return out, nil
}

// parseMLO maps MLO_NETWORK_GET (Wi-Fi 7 multi-link) into a pseudo-band
// "mlo" with a single "mlo" network; nil when MLO is absent.
func parseMLO(raw json.RawMessage) *deco.Band {
	var r struct {
		MLO *struct {
			wireNet
			Band strList `json:"band"`
		} `json:"mlo"`
	}
	if json.Unmarshal(raw, &r) != nil || r.MLO == nil {
		return nil
	}
	return &deco.Band{
		Radio: deco.Radio{Enable: bool(r.MLO.Enable), Channel: strings.Join(r.MLO.Band, "+")},
		Networks: map[string]deco.NetworkConfig{"mlo": {
			Enable: bool(r.MLO.Enable), SSID: deco.DecodeName(r.MLO.SSID),
			Encryption: r.MLO.EncryptionMode, Hidden: bool(r.MLO.HideSSID),
		}},
	}
}

// parseFirmware maps FW_LATEST_GET.
func parseFirmware(raw json.RawMessage) ([]deco.Firmware, error) {
	var r struct {
		FWList json.RawMessage `json:"fw_list"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("firmware: %w", err)
	}
	var out []deco.Firmware
	eachEntry(r.FWList, func(f struct {
		Model        string        `json:"device_model"`
		Version      string        `json:"version"`
		ReleaseDate  string        `json:"release_date"`
		ReleaseNote  string        `json:"release_note"`
		NeedUpgrade  deco.FlexBool `json:"need_to_upgrade"`
		DeviceIDList []string      `json:"device_id_list"`
	}) {
		out = append(out, deco.Firmware{
			Model: f.Model, NodeIDs: f.DeviceIDList,
			LatestVersion: deco.DecodeName(f.Version), ReleaseDate: f.ReleaseDate,
			ReleaseNote: decodeNote(f.ReleaseNote), UpgradeAvailable: bool(f.NeedUpgrade),
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// decodeNote decodes a base64 release note, which may contain newlines
// that DecodeName rejects as non-printable.
func decodeNote(s string) string {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) > 0 && strings.ToValidUTF8(string(b), "") == string(b) {
		return string(b)
	}
	return s
}

// parseWAN maps IPV4_GET. Online comes from the caller (the master node's
// inet status, as the app shows it).
func parseWAN(raw json.RawMessage, online bool) (*deco.WANStatus, error) {
	type ipInfo struct {
		IP      string `json:"ip"`
		Mask    string `json:"mask"`
		Gateway string `json:"gateway"`
		DNS1    string `json:"dns1"`
		DNS2    string `json:"dns2"`
		MAC     string `json:"mac"`
	}
	var r struct {
		WAN struct {
			IPInfo   ipInfo `json:"ip_info"`
			DialType string `json:"dial_type"`
		} `json:"wan"`
		LAN struct {
			IPInfo ipInfo `json:"ip_info"`
		} `json:"lan"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("wan: %w", err)
	}
	w := r.WAN.IPInfo
	return &deco.WANStatus{
		Online: online, MAC: deco.NormalizeMAC(w.MAC), IP: w.IP, Gateway: w.Gateway, Mask: w.Mask,
		DNS1: w.DNS1, DNS2: w.DNS2, DialType: r.WAN.DialType,
		LANIP: r.LAN.IPInfo.IP, LANMask: r.LAN.IPInfo.Mask,
	}, nil
}

// parsePerformance maps DEVICE_RESOURCE_INFO_GET: cpu_usage in percent,
// memory_usage of memory_total. The values may sit at the top level or in
// a device_list entry.
func parsePerformance(raw json.RawMessage) (*deco.Performance, error) {
	type res struct {
		CPU      deco.FlexInt `json:"cpu_usage"`
		MemUsed  deco.FlexInt `json:"memory_usage"`
		MemTotal deco.FlexInt `json:"memory_total"`
	}
	var top struct {
		res
		DeviceList []res `json:"device_list"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("resources: %w", err)
	}
	r := top.res
	if r.CPU == 0 && r.MemTotal == 0 && len(top.DeviceList) > 0 {
		r = top.DeviceList[0]
	}
	p := &deco.Performance{CPUUsage: float64(r.CPU) / 100}
	if r.MemTotal > 0 {
		p.MemUsage = float64(r.MemUsed) / float64(r.MemTotal)
	}
	return p, nil
}

// components returns the component IDs from a COMP_NEGOTIATE result.
func components(neg json.RawMessage) map[string]bool {
	var r struct {
		ComponentList []struct {
			ID string `json:"id"`
		} `json:"component_list"`
	}
	out := map[string]bool{}
	if json.Unmarshal(neg, &r) == nil {
		for _, c := range r.ComponentList {
			out[c.ID] = true
		}
	}
	return out
}
