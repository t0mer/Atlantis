// Package tmpclient implements deco.Client over a TMP/AppV2 session, for
// any transport that yields a byte stream: the TP-Link cloud relay
// (internal/deco/cloud) or SSH on the LAN (internal/deco/tmp/sshconn).
//
// Typed reads map the device's replies into the transport-agnostic deco
// models for every Deco model (see parse.go).
package tmpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/fixture"
	"github.com/t0mer/atlantis/internal/deco/tmp"
)

// Dialer opens the byte stream a session runs on. logf receives transport
// status lines (no secrets).
type Dialer func(ctx context.Context, logf func(string, ...any)) (io.ReadWriteCloser, error)

// RawArea is the area name Raw accepts; the form is an opcode name such as
// "DEVICE_LIST_GET".
const RawArea = "tmp"

// ErrNotImplemented is kept for callers written against the stubs.
//
// Deprecated: every typed read is implemented.
var ErrNotImplemented = errors.New("tmpclient: not implemented")

var _ deco.Client = (*Client)(nil)

// Client is a deco.Client for one Deco network. It is safe for concurrent
// use; requests are serialised by the session.
type Client struct {
	dial Dialer
	opts tmp.Options

	mu   sync.Mutex
	sess *tmp.Session
	neg  json.RawMessage
	logf func(string, ...any)
}

// SetDebug sends transport status lines (codes only, no secrets) to logf.
func (c *Client) SetDebug(logf func(string, ...any)) {
	c.mu.Lock()
	c.logf = logf
	c.mu.Unlock()
}

// New returns a client that opens sessions with dial and opts.
func New(dial Dialer, opts tmp.Options) *Client {
	return &Client{dial: dial, opts: opts}
}

// Login connects to the Deco and opens a TMP session. It is a no-op when a
// session is already open.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil && c.sess.Err() == nil {
		return nil
	}
	if c.sess != nil { // dead session: release it before reconnecting
		c.sess.Close()
		c.sess = nil
	}
	logf := c.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	conn, err := c.dial(ctx, logf)
	if err != nil {
		return err
	}
	sess, neg, err := tmp.Open(ctx, conn, c.opts)
	if err != nil {
		conn.Close()
		return err
	}
	c.sess, c.neg = sess, neg
	return nil
}

// Negotiation returns the COMP_NEGOTIATE result of the open session.
func (c *Client) Negotiation() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.neg
}

// Logout frees the session token and closes the transport.
func (c *Client) Logout(context.Context) error {
	c.mu.Lock()
	sess := c.sess
	c.sess = nil
	c.mu.Unlock()
	if sess == nil {
		return nil
	}
	return sess.Close() // outside c.mu: Close may wait briefly for a request
}

// Request runs an allowlisted opcode, opening a session if needed. When the
// session dies (relay error, broken stream, device bye) it is reopened once,
// except when the device ended it because of its session limits: then
// reconnecting would only fight the Deco app for the slot.
func (c *Client) Request(ctx context.Context, op tmp.Opcode, params any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		sess := c.sess
		c.mu.Unlock()
		if sess == nil {
			return nil, tmp.ErrSessionClosed // logged out concurrently
		}
		res, err := sess.Request(ctx, op, params)
		if err == nil || sess.Err() == nil || attempt > 0 || sessionLimited(sess.Err()) {
			return res, err
		}
	}
}

// sessionLimited reports a bye caused by the device's admin-session limits.
func sessionLimited(err error) bool {
	var bye tmp.ByeError
	if !errors.As(err, &bye) {
		return false
	}
	switch bye.Reason {
	case 3, 7, 16: // client limit, user count exceeded, kicked out
		return true
	}
	return false
}

// Raw runs a read-only opcode by name (area "tmp") and returns its result
// with secret-named fields redacted. Debug use only.
func (c *Client) Raw(ctx context.Context, area, form string) (json.RawMessage, error) {
	return c.RawParams(ctx, area, form, nil)
}

// RawParams is Raw with request params.
func (c *Client) RawParams(ctx context.Context, area, form string, params any) (json.RawMessage, error) {
	if area != RawArea {
		return nil, fmt.Errorf("tmpclient: unknown area %q (want %q)", area, RawArea)
	}
	op, ok := tmp.OpcodeByName(form)
	if !ok {
		return nil, fmt.Errorf("tmpclient: unknown opcode %q", form)
	}
	if tmp.IsWrite(op) {
		return nil, fmt.Errorf("tmpclient: %s changes the device; Raw is read-only", op)
	}
	res, err := c.Request(ctx, op, params)
	if err != nil {
		return nil, err
	}
	return fixture.Redact(res)
}

// Devices lists the mesh nodes (DEVICE_LIST_GET).
func (c *Client) Devices(ctx context.Context) ([]deco.Device, error) {
	raw, err := c.Request(ctx, tmp.OpDeviceListGet, nil)
	if err != nil {
		return nil, err
	}
	return parseDevices(raw)
}

// Clients lists clients, online and offline, with current rates
// (CLIENT_LIST_GET + CLIENT_LIST_SPEED_GET). node filters by Device.ID.
func (c *Client) Clients(ctx context.Context, node string) ([]deco.WifiClient, error) {
	nodes, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	masterID := ""
	for _, n := range nodes {
		if n.Master {
			masterID = n.ID
		}
	}
	raw, err := c.Request(ctx, tmp.OpClientListGet, nil)
	if err != nil {
		return nil, err
	}
	speeds, err := c.Request(ctx, tmp.OpClientListSpeedGet, nil)
	if err != nil {
		speeds = nil // rates are optional; the list itself still counts
	}
	all, err := parseClients(raw, speeds, masterID)
	if err != nil || node == "" {
		return all, err
	}
	var out []deco.WifiClient
	for _, cl := range all {
		if cl.NodeID == node {
			out = append(out, cl)
		}
	}
	return out, nil
}

// Wireless reads every band's networks (WIRELESS_GET), plus the Wi-Fi 7
// multi-link network as band "mlo" when the Deco has one.
func (c *Client) Wireless(ctx context.Context) (*deco.WirelessConfig, error) {
	raw, err := c.Request(ctx, tmp.OpWirelessGet, nil)
	if err != nil {
		return nil, err
	}
	w, err := parseWireless(raw)
	if err != nil {
		return nil, err
	}
	// The component name that gates MLO is not known, so ask and ignore
	// the error a Deco without multi-link answers with.
	if mraw, err := c.Request(ctx, tmp.OpMLONetworkGet, nil); err == nil {
		if b := parseMLO(mraw); b != nil {
			w.Bands["mlo"] = *b
		}
	}
	return w, nil
}

// Firmware reports available updates per model (FW_LATEST_GET).
func (c *Client) Firmware(ctx context.Context) ([]deco.Firmware, error) {
	raw, err := c.Request(ctx, tmp.OpFWLatestGet, nil)
	if err != nil {
		return nil, unsupported(err)
	}
	return parseFirmware(raw)
}

// WAN reads the main unit's WAN addressing (IPV4_GET). Decos without the
// "wan"/"dsl_wan" component (for example in access-point mode) return
// deco.ErrUnsupported, as the app hides the page for them.
func (c *Client) WAN(ctx context.Context) (*deco.WANStatus, error) {
	if !c.hasComponent(ctx, "wan") && !c.hasComponent(ctx, "dsl_wan") {
		return nil, deco.ErrUnsupported
	}
	raw, err := c.Request(ctx, tmp.OpIPv4Get, nil)
	if err != nil {
		return nil, unsupported(err)
	}
	online := false
	if nodes, err := c.Devices(ctx); err == nil {
		for _, n := range nodes {
			if n.Master {
				online = n.InternetOnline
			}
		}
	}
	return parseWAN(raw, online)
}

// Performance reads the main unit's CPU and memory load
// (DEVICE_RESOURCE_INFO_GET). As in the app, it is asked only when the
// Deco negotiated "device_resource"; otherwise deco.ErrUnsupported.
func (c *Client) Performance(ctx context.Context) (*deco.Performance, error) {
	if !c.hasComponent(ctx, "device_resource") {
		return nil, deco.ErrUnsupported
	}
	nodes, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	var master string
	for _, n := range nodes {
		if n.Master {
			master = n.ID
		}
	}
	if master == "" {
		return nil, deco.ErrUnsupported
	}
	raw, err := c.Request(ctx, tmp.OpDeviceResourceInfoGet,
		map[string]any{"device_list": []map[string]string{{"device_id": master}}})
	if err != nil {
		return nil, unsupported(err)
	}
	return parsePerformance(raw)
}

// hasComponent reports whether the session's COMP_NEGOTIATE listed id.
func (c *Client) hasComponent(ctx context.Context, id string) bool {
	if err := c.Login(ctx); err != nil {
		return false
	}
	return components(c.Negotiation())[id]
}

// unsupported maps the device's "business failed" answer (AppV2 err 12)
// and unknown-operation answers to deco.ErrUnsupported.
func unsupported(err error) error {
	var ae tmp.AppV2Error
	if errors.As(err, &ae) && (ae.Code == 12 || ae.Code == 4) {
		return fmt.Errorf("%w (%v)", deco.ErrUnsupported, err)
	}
	return err
}
