// Package deco defines the transport-agnostic model of a TP-Link Deco mesh.
// UI and poller code depend on this package only, never on a transport such
// as internal/deco/luci.
package deco

import (
	"context"
	"encoding/json"
	"errors"
)

// Sentinel errors returned by Client implementations.
var (
	// ErrWrongPassword means the device rejected the management password (-5002).
	// Callers must stop retrying: the device counts attempts and locks out.
	ErrWrongPassword = errors.New("deco: wrong management password")
	// ErrOtherAdmin means another admin session is active on the device (HTTP 403 at login).
	ErrOtherAdmin = errors.New("deco: another admin session is active")
	// ErrCertChanged means the pinned TLS certificate or SSH host key no longer matches.
	ErrCertChanged = errors.New("deco: TLS certificate fingerprint changed")
	// ErrCertUnknown means no certificate or host key is pinned yet and the
	// transport will not trust one without an explicit accept.
	ErrCertUnknown = errors.New("deco: certificate or host key not trusted yet")
	// ErrAuthFailed is returned without contacting the device after ErrWrongPassword,
	// until the client is reset with a new password.
	ErrAuthFailed = errors.New("deco: authentication previously failed; reset credentials")
)

// ErrUnsupported means the device does not offer this information (for
// example WAN details on a Deco in access-point mode). It is not a session
// or transport error.
var ErrUnsupported = errors.New("deco: not supported by this device")

// Backhaul types: how a node reaches its parent.
const (
	BackhaulWired   = "wired"
	BackhaulWiFi    = "wifi"
	BackhaulPLC     = "plc"      // powerline
	BackhaulPLCWiFi = "plc+wifi" // powerline and Wi-Fi together
	BackhaulOther   = "other"    // zigbee, bluetooth, or a value this code does not know
)

// Backhaul describes a node's link to its parent. The master has none.
type Backhaul struct {
	Type string `json:"type"`
	// Links are the device's own connection_type values, e.g. "band5",
	// "wired", "plc" or a multi-link combination such as "band2+band5".
	Links []string `json:"links"`
	// Signal maps a band (band2_4, band5_1, band5_2, band6, band6_2) to a
	// level in bars; 3 or more is strong, 2 medium, less weak.
	Signal map[string]int `json:"signal"`
}

// Device is one mesh node (main unit or satellite).
type Device struct {
	ID          string `json:"id"`  // device_id on TMP; the MAC on the local web API
	MAC         string `json:"mac"` // aa:bb:cc:dd:ee:ff
	IP          string `json:"ip"`
	Nickname    string `json:"nickname"`
	Model       string `json:"model"`
	HardwareVer string `json:"hardware_ver"`
	SoftwareVer string `json:"software_ver"`
	Master      bool   `json:"master"`
	// Online reports the node as connected to the mesh.
	Online bool `json:"online"`
	// InternetOnline reports the node's internet as working.
	InternetOnline bool `json:"internet_online"`
	// InternetError is the device's reason when the internet is down, e.g.
	// "wan_port_unplugged" or "sim_absent" (empty when online).
	InternetError string   `json:"internet_error"`
	ParentID      string   `json:"parent_id"` // the upstream node's ID; empty for the master
	Backhaul      Backhaul `json:"backhaul"`
	// Raw status strings, kept so values this code does not know are not lost.
	Role        string `json:"role"`
	GroupStatus string `json:"group_status"`
	InetStatus  string `json:"inet_status"`
}

// Client network names (the device's "interface").
const (
	NetworkMain  = "main"
	NetworkGuest = "guest"
	NetworkIoT   = "iot"
	NetworkMLO   = "mlo"
)

// WifiClient is a client device seen by the mesh, online or offline.
type WifiClient struct {
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Type    string `json:"type"` // phone, laptop, iot_device, ... when the device reports one
	Online  bool   `json:"online"`
	Network string `json:"network"` // main, guest, iot, mlo, or as reported
	Wired   bool   `json:"wired"`
	// Band is the Wi-Fi band in the WIRELESS band naming (band2_4,
	// band5_1, band6, ...); empty for wired clients.
	Band   string `json:"band"`
	NodeID string `json:"node_id"` // the node the client is attached to; the master if unknown
	// SignalLevel is in bars (3+ strong); 0 when unknown or wired.
	SignalLevel int `json:"signal_level"`
	// UpBps and DownBps are the current rates in bits per second.
	UpBps   int64 `json:"up_bps"`
	DownBps int64 `json:"down_bps"`
}

// WANStatus describes the main unit's internet connection.
type WANStatus struct {
	Online   bool   `json:"online"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Gateway  string `json:"gateway"`
	Mask     string `json:"mask"`
	DNS1     string `json:"dns1"`
	DNS2     string `json:"dns2"`
	DialType string `json:"dial_type"` // dynamic_ip, static_ip, pppoe, ...
	LANIP    string `json:"lan_ip"`
	LANMask  string `json:"lan_mask"`
}

// Performance is the main unit's load.
type Performance struct {
	CPUUsage float64 `json:"cpu_usage"` // 0..1
	MemUsage float64 `json:"mem_usage"` // 0..1
}

// NetworkConfig is one Wi-Fi network on a band. The password is
// deliberately absent: it is never returned to callers.
type NetworkConfig struct {
	Enable     bool   `json:"enable"`
	SSID       string `json:"ssid"`
	Encryption string `json:"encryption"`
	Hidden     bool   `json:"hidden"`
}

// Radio is one band's radio.
type Radio struct {
	Enable  bool   `json:"enable"`
	Channel string `json:"channel"` // "auto" or a channel number
}

// Band is one Wi-Fi band and its networks (host, guest, iot, ...).
type Band struct {
	Radio    Radio                    `json:"radio"`
	Networks map[string]NetworkConfig `json:"networks"`
}

// WirelessConfig is keyed by the device's own band names (band2_4,
// band5_1, band5_2, band6, band6_2, band60), so new bands need no code.
type WirelessConfig struct {
	Bands map[string]Band `json:"bands"`
}

// Firmware is the update state for one model of node in the mesh.
type Firmware struct {
	Model            string   `json:"model"`
	NodeIDs          []string `json:"node_ids"`
	LatestVersion    string   `json:"latest_version"`
	ReleaseDate      string   `json:"release_date"`
	ReleaseNote      string   `json:"release_note"`
	UpgradeAvailable bool     `json:"upgrade_available"`
}

// Client is the device API used by the poller and the HTTP layer.
type Client interface {
	Login(ctx context.Context) error
	Logout(ctx context.Context) error
	Devices(ctx context.Context) ([]Device, error)
	// Clients lists clients; node is a Device.ID, or "" for all nodes.
	Clients(ctx context.Context, node string) ([]WifiClient, error)
	WAN(ctx context.Context) (*WANStatus, error)
	Performance(ctx context.Context) (*Performance, error)
	Wireless(ctx context.Context) (*WirelessConfig, error)
	Firmware(ctx context.Context) ([]Firmware, error)
	// Raw performs a read-only call and returns the decrypted result. Debug use only.
	Raw(ctx context.Context, area, form string) (json.RawMessage, error)
}

// WiFiWriter is implemented by transports that can change the main Wi-Fi
// network (verified on hardware). SetHostWiFi returns the changed bands.
type WiFiWriter interface {
	SetHostWiFi(ctx context.Context, ssid, password string) ([]string, error)
}

// NormalizeBand maps the device-list/client naming ("band5") to the
// WIRELESS naming ("band5_1"); other names pass through.
func NormalizeBand(b string) string {
	if b == "band5" {
		return "band5_1"
	}
	return b
}
