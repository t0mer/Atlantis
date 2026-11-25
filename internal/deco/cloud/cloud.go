// Package cloud implements deco.Client for one Deco site reached through the
// TP-Link cloud: the cloud relay (ata) carries a TMP/AppV2 session to the
// site's main Deco.
//
// Typed reads return ErrNotImplemented until their responses have been
// recorded from real hardware (CLAUDE.md §12.2); Raw works now.
package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/fixture"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/ata"
	"github.com/t0mer/atlantis/internal/tplinkcloud"
)

// RawArea is the area name Raw accepts; the form is an opcode name such as
// "DEVICE_LIST_GET".
const RawArea = "tmp"

// ErrNotImplemented is returned by typed reads not yet mapped from fixtures.
var ErrNotImplemented = errors.New("cloud: not implemented until fixtures are recorded")

var _ deco.Client = (*Client)(nil)

// Client is a deco.Client for one site. It is safe for concurrent use;
// requests are serialised by the session.
type Client struct {
	account *tplinkcloud.Client
	site    tplinkcloud.Site

	mu   sync.Mutex
	sess *tmp.Session
	neg  json.RawMessage
	logf func(string, ...any)
}

// SetDebug sends relay status lines (codes only, no secrets) to logf.
func (c *Client) SetDebug(logf func(string, ...any)) {
	c.mu.Lock()
	c.logf = logf
	c.mu.Unlock()
}

// New returns a client for site. account must already be logged in.
func New(account *tplinkcloud.Client, site tplinkcloud.Site) *Client {
	return &Client{account: account, site: site}
}

// Login connects to the site's main Deco and opens a TMP session. It is a
// no-op when a session is already open.
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
	conn, err := ata.Dial(ctx, c.account, c.site.AppServerURL, c.site.MasterDeviceID, c.logf)
	if err != nil {
		return err
	}
	sess, neg, err := tmp.Open(ctx, conn, tmp.Options{Username: c.account.Email(), Owner: c.site.Owner})
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

// Logout frees the session token and disconnects the relay.
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
		return nil, fmt.Errorf("cloud: unknown area %q (want %q)", area, RawArea)
	}
	op, ok := tmp.OpcodeByName(form)
	if !ok {
		return nil, fmt.Errorf("cloud: unknown opcode %q", form)
	}
	res, err := c.Request(ctx, op, params)
	if err != nil {
		return nil, err
	}
	return fixture.Redact(res)
}

func (c *Client) Devices(context.Context) ([]deco.Device, error) { return nil, ErrNotImplemented }
func (c *Client) Clients(context.Context, string) ([]deco.WifiClient, error) {
	return nil, ErrNotImplemented
}
func (c *Client) WAN(context.Context) (*deco.WANStatus, error) { return nil, ErrNotImplemented }
func (c *Client) Performance(context.Context) (*deco.Performance, error) {
	return nil, ErrNotImplemented
}
func (c *Client) Wireless(context.Context) (*deco.WirelessConfig, error) {
	return nil, ErrNotImplemented
}
