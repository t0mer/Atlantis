// Package cloud reaches one Deco site through the TP-Link cloud: the cloud
// relay (ata) carries a TMP/AppV2 session to the site's main Deco.
package cloud

import (
	"context"
	"io"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/ata"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
	"github.com/t0mer/atlantis/internal/tplinkcloud"
)

// RawArea and ErrNotImplemented are re-exported from tmpclient.
const RawArea = tmpclient.RawArea

var ErrNotImplemented = tmpclient.ErrNotImplemented

// Client is the session client a site uses.
type Client = tmpclient.Client

// New returns a deco.Client for site. account must already be logged in.
func New(account *tplinkcloud.Client, site tplinkcloud.Site) *tmpclient.Client {
	dial := func(ctx context.Context, logf func(string, ...any)) (io.ReadWriteCloser, error) {
		return ata.Dial(ctx, account, site.AppServerURL, site.MasterDeviceID, logf)
	}
	return tmpclient.New(dial, tmp.Options{Username: account.Email(), Owner: site.Owner})
}
