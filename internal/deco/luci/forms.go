package luci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/t0mer/atlantis/internal/deco"
)

// Read-only forms (CLAUDE.md §4.1.6). Write forms are added only after the
// Phase 0 spike confirms them on hardware.

var (
	_ deco.Client = (*Client)(nil)

	errorDecodingRE = regexp.MustCompile(`^<Error Decoding (.*)>$`)
)

// decodeName turns a device-provided name into UTF-8. Names are base64; if
// base64 decoding fails the raw value is used, with legacy "<Error Decoding X>"
// wrappers unwrapped to X.
func decodeName(s string) string {
	if s == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && utf8.Valid(b) {
		return string(b)
	}
	if m := errorDecodingRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// normalizeMAC converts the wire form AA-BB-CC-DD-EE-FF to aa:bb:cc:dd:ee:ff.
func normalizeMAC(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", ":"))
}

// wireMAC converts aa:bb:cc:dd:ee:ff to the device's AA-BB-CC-DD-EE-FF form.
func wireMAC(s string) string {
	return strings.ToUpper(strings.ReplaceAll(s, ":", "-"))
}

type wireDevice struct {
	MAC            string `json:"mac"`
	Nickname       string `json:"nickname"`
	CustomNickname string `json:"custom_nickname"`
	Role           string `json:"role"`
	DeviceModel    string `json:"device_model"`
	DeviceIP       string `json:"device_ip"`
	HardwareVer    string `json:"hardware_ver"`
	SoftwareVer    string `json:"software_ver"`
	GroupStatus    int    `json:"group_status"`
	InetStatus     int    `json:"inet_status"`
	Signal         struct {
		Band24 int `json:"band2_4"`
		Band5  int `json:"band5"`
	} `json:"signal_strength"`
	RxRateList []int  `json:"rx_rate_list"`
	TxRateList []int  `json:"tx_rate_list"`
	Previous   string `json:"previous"`
	PortCount  int    `json:"port_count"`
}

// Devices lists the mesh nodes (area device, form device_list).
func (c *Client) Devices(ctx context.Context) ([]deco.Device, error) {
	raw, err := c.call(ctx, "device", "device_list", "read", nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		DeviceList []wireDevice `json:"device_list"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]deco.Device, 0, len(w.DeviceList))
	for _, d := range w.DeviceList {
		name := decodeName(d.CustomNickname)
		if name == "" {
			name = decodeName(d.Nickname)
		}
		out = append(out, deco.Device{
			MAC:         normalizeMAC(d.MAC),
			Nickname:    name,
			Role:        d.Role,
			Model:       d.DeviceModel,
			IP:          d.DeviceIP,
			HardwareVer: d.HardwareVer,
			SoftwareVer: d.SoftwareVer,
			GroupStatus: d.GroupStatus,
			InetStatus:  d.InetStatus,
			Signal24:    d.Signal.Band24,
			Signal5:     d.Signal.Band5,
			RxRates:     d.RxRateList,
			TxRates:     d.TxRateList,
			Previous:    normalizeMAC(d.Previous),
			PortCount:   d.PortCount,
		})
	}
	return out, nil
}

type wireClient struct {
	MAC            string `json:"mac"`
	IP             string `json:"ip"`
	Name           string `json:"name"`
	Online         bool   `json:"online"`
	WireType       string `json:"wire_type"`
	ConnectionType string `json:"connection_type"`
	Interface      string `json:"interface"`
	UpSpeed        int64  `json:"up_speed"`
	DownSpeed      int64  `json:"down_speed"`
}

// Clients lists Wi-Fi/wired clients seen by one node, or by all nodes when nodeMAC is empty
// (area client, form client_list, device_mac "default").
func (c *Client) Clients(ctx context.Context, nodeMAC string) ([]deco.WifiClient, error) {
	mac := "default"
	if nodeMAC != "" {
		mac = wireMAC(nodeMAC)
	}
	raw, err := c.call(ctx, "client", "client_list", "read", map[string]string{"device_mac": mac})
	if err != nil {
		return nil, err
	}
	var w struct {
		ClientList []wireClient `json:"client_list"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]deco.WifiClient, 0, len(w.ClientList))
	for _, cl := range w.ClientList {
		out = append(out, deco.WifiClient{
			MAC:            normalizeMAC(cl.MAC),
			IP:             cl.IP,
			Name:           decodeName(cl.Name),
			Online:         cl.Online,
			WireType:       cl.WireType,
			ConnectionType: cl.ConnectionType,
			Interface:      cl.Interface,
			UpSpeed:        cl.UpSpeed,
			DownSpeed:      cl.DownSpeed,
		})
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
	var w map[string]map[string]struct {
		Enable bool   `json:"enable"`
		SSID   string `json:"ssid"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := &deco.WirelessConfig{Bands: make(map[string]map[string]deco.NetworkConfig, len(w))}
	for band, nets := range w {
		out.Bands[band] = make(map[string]deco.NetworkConfig, len(nets))
		for name, n := range nets {
			out.Bands[band][name] = deco.NetworkConfig{Enable: n.Enable, SSID: decodeName(n.SSID)}
		}
	}
	return out, nil
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
	return redactJSON(raw)
}

var secretKeys = map[string]bool{"password": true, "passwd": true, "stok": true, "token": true}

// redactJSON replaces the value of every secret-named key, at any depth.
func redactJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	redactValue(v)
	return json.Marshal(v)
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if secretKeys[strings.ToLower(k)] {
				t[k] = "[redacted]"
			} else {
				redactValue(val)
			}
		}
	case []any:
		for _, e := range t {
			redactValue(e)
		}
	}
}
