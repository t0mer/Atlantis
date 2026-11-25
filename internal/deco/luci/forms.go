package luci

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/fixture"
)

// Read-only forms (CLAUDE.md §4.1.6). Write forms are added only after the
// Phase 0 spike confirms them on hardware.

var _ deco.Client = (*Client)(nil)

// Name and MAC helpers are shared with the other transports.
var (
	decodeName   = deco.DecodeName
	normalizeMAC = deco.NormalizeMAC
	wireMAC      = deco.WireMAC
)

// Devices lists the mesh nodes (area device, form device_list). The local
// API identifies nodes by MAC, so Device.ID is the MAC. Fields are read
// leniently (deco.Obj): an odd value never drops a node.
func (c *Client) Devices(ctx context.Context) ([]deco.Device, error) {
	raw, err := c.call(ctx, "device", "device_list", "read", nil)
	if err != nil {
		return nil, err
	}
	top, _ := deco.ParseObj(raw)
	var out []deco.Device
	for _, d := range top.Objs("device_list") {
		mac := normalizeMAC(d.Str("mac"))
		if mac == "" {
			continue
		}
		name := decodeName(d.Str("custom_nickname"))
		if name == "" {
			name = decodeName(d.Str("nickname"))
		}
		role, group, inet := d.Str("role"), d.Str("group_status"), d.Str("inet_status")
		dev := deco.Device{
			ID: mac, MAC: mac, IP: d.Str("device_ip"), Nickname: name, Model: d.Str("device_model"),
			HardwareVer: d.Str("hardware_ver"), SoftwareVer: d.Str("software_ver"),
			Master: strings.EqualFold(role, "master"),
			// Older firmware sends numeric statuses; treat 1 as up.
			Online:         !strings.EqualFold(group, "disconnected") && group != "0",
			InternetOnline: strings.EqualFold(inet, "online") || inet == "1",
			Role:           role, GroupStatus: group, InetStatus: inet,
		}
		if !dev.Master {
			dev.ParentID = normalizeMAC(d.Str("previous"))
			// The local API does not say how a satellite is linked; the
			// backhaul type stays unknown rather than guessed.
			if sig := d.Ints("signal_strength"); len(sig) > 0 {
				dev.Backhaul.Signal = map[string]int{}
				for band, v := range sig {
					dev.Backhaul.Signal[deco.NormalizeBand(band)] = v
				}
			}
		}
		out = append(out, dev)
	}
	return out, nil
}

// Clients lists clients seen by one node (a Device.ID, i.e. its MAC here),
// or by all nodes when node is empty (area client, form client_list); a
// client with no node is attributed to the master. Speeds are taken as kbps
// like the TMP API; unverified on luci firmware.
func (c *Client) Clients(ctx context.Context, node string) ([]deco.WifiClient, error) {
	mac := "default"
	if node != "" {
		mac = wireMAC(node)
	} else if devs, err := c.Devices(ctx); err == nil {
		for _, d := range devs {
			if d.Master {
				node = d.ID
			}
		}
	}
	raw, err := c.call(ctx, "client", "client_list", "read", map[string]string{"device_mac": mac})
	if err != nil {
		return nil, err
	}
	top, _ := deco.ParseObj(raw)
	var out []deco.WifiClient
	for _, cl := range top.Objs("client_list") {
		cm := normalizeMAC(cl.Str("mac"))
		if cm == "" {
			continue
		}
		wired := strings.EqualFold(cl.Str("wire_type"), "wired")
		wc := deco.WifiClient{
			MAC: cm, IP: cl.Str("ip"), Name: decodeName(cl.Str("name")),
			Online: cl.Bool("online"), Network: cl.Str("interface"), Wired: wired, NodeID: node,
			UpBps: cl.Int("up_speed") * 1000, DownBps: cl.Int("down_speed") * 1000,
		}
		if !wired {
			wc.Band = deco.NormalizeBand(cl.Str("connection_type"))
		}
		out = append(out, wc)
	}
	return out, nil
}

// WAN reads WAN and LAN addressing (area network, form wan_ipv4).
func (c *Client) WAN(ctx context.Context) (*deco.WANStatus, error) {
	raw, err := c.call(ctx, "network", "wan_ipv4", "read", nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		WAN struct {
			IPInfo struct {
				MAC     string `json:"mac"`
				IP      string `json:"ip"`
				Gateway string `json:"gateway"`
				Mask    string `json:"mask"`
				DNS1    string `json:"dns1"`
				DNS2    string `json:"dns2"`
			} `json:"ip_info"`
			DialType string `json:"dial_type"`
		} `json:"wan"`
		LAN struct {
			IPInfo struct {
				IP   string `json:"ip"`
				Mask string `json:"mask"`
			} `json:"ip_info"`
		} `json:"lan"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	ip := w.WAN.IPInfo
	return &deco.WANStatus{
		MAC:      normalizeMAC(ip.MAC),
		IP:       ip.IP,
		Gateway:  ip.Gateway,
		Mask:     ip.Mask,
		DNS1:     ip.DNS1,
		DNS2:     ip.DNS2,
		DialType: w.WAN.DialType,
		LANIP:    w.LAN.IPInfo.IP,
		LANMask:  w.LAN.IPInfo.Mask,
	}, nil
}

// Performance reads master CPU and memory load (area network, form performance).
func (c *Client) Performance(ctx context.Context) (*deco.Performance, error) {
	raw, err := c.call(ctx, "network", "performance", "read", nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		CPU float64 `json:"cpu_usage"`
		Mem float64 `json:"mem_usage"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return &deco.Performance{CPUUsage: w.CPU, MemUsage: w.Mem}, nil
}

// Wireless reads per-band Wi-Fi state (area wireless, form wlan). Passwords are
// read off the wire and dropped; they never enter the model.
func (c *Client) Wireless(ctx context.Context) (*deco.WirelessConfig, error) {
	raw, err := c.call(ctx, "wireless", "wlan", "read", nil)
	if err != nil {
		return nil, err
	}
	top, _ := deco.ParseObj(raw)
	out := &deco.WirelessConfig{Bands: map[string]deco.Band{}}
	for band := range top {
		if !deco.IsBand(band) {
			continue
		}
		nets, ok := deco.ParseObj(top[band])
		if !ok {
			continue
		}
		b := deco.Band{Networks: map[string]deco.NetworkConfig{}}
		for name := range nets {
			n, ok := deco.ParseObj(nets[name])
			if !ok {
				continue
			}
			b.Networks[name] = deco.NetworkConfig{Enable: n.Bool("enable"), SSID: decodeName(n.Str("ssid"))}
		}
		out.Bands[band] = b
	}
	return out, nil
}

// Firmware is not available through the local web API.
func (c *Client) Firmware(context.Context) ([]deco.Firmware, error) {
	return nil, deco.ErrUnsupported
}

// Raw returns the decrypted result of a read-only call, with secret-bearing
// fields (passwords, tokens) replaced by "[redacted]". Debug use only.
func (c *Client) Raw(ctx context.Context, area, form string) (json.RawMessage, error) {
	return c.RawParams(ctx, area, form, nil)
}

// RawParams is Raw with request params, for forms that need them (client_list).
func (c *Client) RawParams(ctx context.Context, area, form string, params any) (json.RawMessage, error) {
	raw, err := c.call(ctx, area, form, "read", params)
	if err != nil {
		return nil, err
	}
	return fixture.Redact(raw)
}
