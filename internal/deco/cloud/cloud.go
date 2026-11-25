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
	if c.sess != nil {
		return nil
	}
	conn, err := ata.Dial(ctx, c.account, c.site.AppServerURL, c.site.MasterDeviceID)
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
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil
	}
	err := c.sess.Close()
	c.sess = nil
	return err
}

// Request runs an allowlisted opcode, opening a session if needed. A
// session the device closed (bye) is dropped and reopened once.
func (c *Client) Request(ctx context.Context, op tmp.Opcode, params any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		sess := c.sess
		c.mu.Unlock()
		res, err := sess.Request(ctx, op, params)
		var bye tmp.ByeError
		if errors.As(err, &bye) && attempt == 0 && bye.Reason != 3 && bye.Reason != 7 && bye.Reason != 16 {
			c.Logout(ctx) // reconnect, except when we were pushed out by session limits
			continue
		}
		return res, err
	}
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
