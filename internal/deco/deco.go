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
	Type string
	// Links are the device's own connection_type values, e.g. "band5",
	// "wired", "plc" or a multi-link combination such as "band2+band5".
	Links []string
	// Signal maps a band (band2_4, band5_1, band5_2, band6, band6_2) to a
	// level in bars; 3 or more is strong, 2 medium, less weak.
	Signal map[string]int
}

// Device is one mesh node (main unit or satellite).
type Device struct {
	ID          string // device_id on TMP; the MAC on the local web API
	MAC         string // aa:bb:cc:dd:ee:ff
	IP          string
	Nickname    string
	Model       string
	HardwareVer string
	SoftwareVer string
	Master      bool
	// Online reports the node as connected to the mesh.
	Online bool
	// InternetOnline reports the node's internet as working.
	InternetOnline bool
	// InternetError is the device's reason when the internet is down, e.g.
	// "wan_port_unplugged" or "sim_absent" (empty when online).
	InternetError string
	ParentID      string // the upstream node's ID; empty for the master
	Backhaul      Backhaul
	// Raw status strings, kept so values this code does not know are not lost.
	Role, GroupStatus, InetStatus string
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
	MAC     string
	IP      string
	Name    string
	Type    string // phone, laptop, iot_device, ... when the device reports one
	Online  bool
	Network string // main, guest, iot, mlo, or as reported
	Wired   bool
	// Band is the Wi-Fi band in the WIRELESS band naming (band2_4,
	// band5_1, band6, ...); empty for wired clients.
	Band   string
	NodeID string // the node the client is attached to; the master if unknown
	// SignalLevel is in bars (3+ strong); 0 when unknown or wired.
	SignalLevel int
	// UpBps and DownBps are the current rates in bits per second.
	UpBps, DownBps int64
}

// WANStatus describes the main unit's internet connection.
type WANStatus struct {
	Online   bool
	MAC      string
	IP       string
	Gateway  string
	Mask     string
	DNS1     string
	DNS2     string
	DialType string // dynamic_ip, static_ip, pppoe, ...
	LANIP    string
	LANMask  string
}

// Performance is the main unit's load.
type Performance struct {
	CPUUsage float64 // 0..1
	MemUsage float64 // 0..1
}

// NetworkConfig is one Wi-Fi network on a band. The password is
// deliberately absent: it is never returned to callers.
type NetworkConfig struct {
	Enable     bool
	SSID       string
	Encryption string
	Hidden     bool
}

// Radio is one band's radio.
type Radio struct {
	Enable  bool
	Channel string // "auto" or a channel number
}

// Band is one Wi-Fi band and its networks (host, guest, iot, ...).
type Band struct {
	Radio    Radio
	Networks map[string]NetworkConfig
}

// WirelessConfig is keyed by the device's own band names (band2_4,
// band5_1, band5_2, band6, band6_2, band60), so new bands need no code.
type WirelessConfig struct {
	Bands map[string]Band
}

// Firmware is the update state for one model of node in the mesh.
type Firmware struct {
	Model            string
	NodeIDs          []string
	LatestVersion    string
	ReleaseDate      string
	ReleaseNote      string
	UpgradeAvailable bool
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
