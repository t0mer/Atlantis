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

type wireDevice struct {
	MAC            string                  `json:"mac"`
	Nickname       string                  `json:"nickname"`
	CustomNickname string                  `json:"custom_nickname"`
	Role           string                  `json:"role"`
	DeviceModel    string                  `json:"device_model"`
	DeviceIP       string                  `json:"device_ip"`
	HardwareVer    string                  `json:"hardware_ver"`
	SoftwareVer    string                  `json:"software_ver"`
	GroupStatus    string                  `json:"group_status"`
	InetStatus     string                  `json:"inet_status"`
	Signal         map[string]deco.FlexInt `json:"signal_strength"`
	Previous       string                  `json:"previous"`
}

// Devices lists the mesh nodes (area device, form device_list). The local
// API identifies nodes by MAC, so Device.ID is the MAC; status strings
// follow the same values as the TMP device list.
func (c *Client) Devices(ctx context.Context) ([]deco.Device, error) {
	raw, err := c.call(ctx, "device", "device_list", "read", nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		DeviceList []json.RawMessage `json:"device_list"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]deco.Device, 0, len(w.DeviceList))
	for _, item := range w.DeviceList {
		var d wireDevice
		if json.Unmarshal(item, &d) != nil {
			continue // one unreadable node must not hide the others
		}
		name := decodeName(d.CustomNickname)
		if name == "" {
			name = decodeName(d.Nickname)
		}
		mac := normalizeMAC(d.MAC)
		dev := deco.Device{
			ID: mac, MAC: mac, IP: d.DeviceIP, Nickname: name, Model: d.DeviceModel,
			HardwareVer: d.HardwareVer, SoftwareVer: d.SoftwareVer,
			Master:         strings.EqualFold(d.Role, "master"),
			Online:         !strings.EqualFold(d.GroupStatus, "disconnected"),
			InternetOnline: strings.EqualFold(d.InetStatus, "online"),
			Role:           d.Role, GroupStatus: d.GroupStatus, InetStatus: d.InetStatus,
		}
		if !dev.Master {
			dev.ParentID = normalizeMAC(d.Previous)
			dev.Backhaul = deco.Backhaul{Type: deco.BackhaulWiFi}
			if len(d.Signal) > 0 {
				dev.Backhaul.Signal = map[string]int{}
				for band, v := range d.Signal {
					dev.Backhaul.Signal[deco.NormalizeBand(band)] = int(v)
				}
			}
		}
		out = append(out, dev)
	}
	return out, nil
}

type wireClient struct {
	MAC            string        `json:"mac"`
	IP             string        `json:"ip"`
	Name           string        `json:"name"`
	Online         deco.FlexBool `json:"online"`
	WireType       string        `json:"wire_type"`
	ConnectionType string        `json:"connection_type"`
	Interface      string        `json:"interface"`
	UpSpeed        deco.FlexInt  `json:"up_speed"`
	DownSpeed      deco.FlexInt  `json:"down_speed"`
}

// Clients lists clients seen by one node (a Device.ID, i.e. its MAC here),
// or by all nodes when node is empty (area client, form client_list).
// Speeds are taken as kbps like the TMP API; unverified on luci firmware.
func (c *Client) Clients(ctx context.Context, node string) ([]deco.WifiClient, error) {
	mac := "default"
	if node != "" {
		mac = wireMAC(node)
	}
	raw, err := c.call(ctx, "client", "client_list", "read", map[string]string{"device_mac": mac})
	if err != nil {
		return nil, err
	}
	var w struct {
		ClientList []json.RawMessage `json:"client_list"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]deco.WifiClient, 0, len(w.ClientList))
	for _, item := range w.ClientList {
		var cl wireClient
		if json.Unmarshal(item, &cl) != nil {
			continue
		}
		wired := strings.EqualFold(cl.WireType, "wired")
		c := deco.WifiClient{
			MAC: normalizeMAC(cl.MAC), IP: cl.IP, Name: decodeName(cl.Name),
			Online: bool(cl.Online), Network: cl.Interface, Wired: wired, NodeID: node,
			UpBps: int64(cl.UpSpeed) * 1000, DownBps: int64(cl.DownSpeed) * 1000,
		}
		if !wired {
			c.Band = deco.NormalizeBand(cl.ConnectionType)
		}
		out = append(out, c)
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
	var w map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := &deco.WirelessConfig{Bands: make(map[string]deco.Band, len(w))}
	for band, nets := range w {
		b := deco.Band{Networks: make(map[string]deco.NetworkConfig, len(nets))}
		for name, nraw := range nets {
			var n struct {
				Enable deco.FlexBool `json:"enable"`
				SSID   string        `json:"ssid"`
			}
			if json.Unmarshal(nraw, &n) != nil {
				continue
			}
			b.Networks[name] = deco.NetworkConfig{Enable: bool(n.Enable), SSID: decodeName(n.SSID)}
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
