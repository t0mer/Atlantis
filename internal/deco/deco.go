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
	// ErrCertChanged means the TLS certificate fingerprint no longer matches the pinned one.
	ErrCertChanged = errors.New("deco: TLS certificate fingerprint changed")
	// ErrAuthFailed is returned without contacting the device after ErrWrongPassword,
	// until the client is reset with a new password.
	ErrAuthFailed = errors.New("deco: authentication previously failed; reset credentials")
)

// Device is one mesh node (router or satellite).
type Device struct {
	MAC         string
	Nickname    string
	Role        string // "master" or "slave"
	Model       string
	IP          string
	HardwareVer string
	SoftwareVer string
	GroupStatus int
	InetStatus  int
	Signal24    int
	Signal5     int
	RxRates     []int
	TxRates     []int
	Previous    string // MAC of the upstream node (backhaul parent)
	PortCount   int
}

// WifiClient is a client device seen by the mesh.
type WifiClient struct {
	MAC            string
	IP             string
	Name           string
	Online         bool
	WireType       string
	ConnectionType string
	Interface      string // "main" or "guest"
	UpSpeed        int64
	DownSpeed      int64
}

// WANStatus describes the master node's WAN and LAN addressing.
type WANStatus struct {
	MAC      string
	IP       string
	Gateway  string
	Mask     string
	DNS1     string
	DNS2     string
	DialType string
	LANIP    string
	LANMask  string
}

// Performance is the master node's load.
type Performance struct {
	CPUUsage float64 // 0..1
	MemUsage float64 // 0..1
}

// NetworkConfig is one Wi-Fi network (host or guest) on a band.
// The password is deliberately absent: it is never returned to callers.
type NetworkConfig struct {
	Enable bool
	SSID   string
}

// WirelessConfig maps band name (e.g. "band2_4") and network ("host"/"guest") to its config.
type WirelessConfig struct {
	Bands map[string]map[string]NetworkConfig
}

// Client is the device API used by the poller and the HTTP layer.
type Client interface {
	Login(ctx context.Context) error
	Logout(ctx context.Context) error
	Devices(ctx context.Context) ([]Device, error)
	Clients(ctx context.Context, nodeMAC string) ([]WifiClient, error)
	WAN(ctx context.Context) (*WANStatus, error)
	Performance(ctx context.Context) (*Performance, error)
	Wireless(ctx context.Context) (*WirelessConfig, error)
	// Raw performs a read-only call and returns the decrypted result. Debug use only.
	Raw(ctx context.Context, area, form string) (json.RawMessage, error)
}
