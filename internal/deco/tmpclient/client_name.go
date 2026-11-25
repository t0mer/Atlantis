package tmpclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
)

// clientRenameVerified is set once `atlantis ssh client-rename-probe` has
// passed on a real Deco (record: testdata/m5/client_rename_probe.json).
var clientRenameVerified = true

// renameFields are the fields the app's rename screen copies from the
// current client into the sparse entry it sends (ClientNameSettingActivity);
// each is sent only if the device reported it.
var renameFields = []string{"client_type", "enable_priority", "remain_time", "space_id"}

// CanRenameClients reports whether renaming is verified on hardware.
func (c *Client) CanRenameClients() bool { return clientRenameVerified }

// RenameClient sets the client's name on the Deco, as the Deco app does.
func (c *Client) RenameClient(ctx context.Context, mac, name string) error {
	if !clientRenameVerified {
		return fmt.Errorf("%w: renaming a client", deco.ErrNotVerified)
	}
	return c.renameClient(ctx, mac, name, true)
}

// ProbeRenameClient is RenameClient without the verified check, with the
// "set by the user" flag given explicitly (to restore an original). It
// exists for `atlantis ssh client-rename-probe`; nothing else may call it.
func (c *Client) ProbeRenameClient(ctx context.Context, mac, name string, userSet bool) error {
	return c.renameClient(ctx, mac, name, userSet)
}

// ClientEntry returns the device's entry for mac from CLIENT_LIST_GET.
func (c *Client) ClientEntry(ctx context.Context, mac string) (deco.Obj, error) {
	raw, err := c.Request(ctx, tmp.OpClientListGet, nil)
	if err != nil {
		return nil, err
	}
	top, _ := deco.ParseObj(raw)
	want := deco.NormalizeMAC(mac)
	for _, e := range top.Objs("client_list") {
		if deco.NormalizeMAC(e.Str("mac")) == want {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: the Deco does not list %s right now", deco.ErrNotFound, want)
}

// ValidateClientName checks a name before it is sent.
func ValidateClientName(name string) error {
	if n := len(name); n < 1 || n > 64 || !utf8.ValidString(name) {
		return deco.Invalid("a device name must be 1-64 bytes of text (got %d bytes)", n)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return deco.Invalid("a device name must not contain control characters")
		}
	}
	return nil
}

// renameEntry is the sparse client entry the app sends to rename.
func renameEntry(cur deco.Obj, name string, userSet bool) map[string]any {
	e := map[string]any{
		"mac":                cur.Str("mac"), // exactly as the device reports it
		"name":               base64.StdEncoding.EncodeToString([]byte(name)),
		"user_set_name_type": userSet,
	}
	for _, k := range renameFields {
		if v, ok := cur[k]; ok {
			e[k] = json.RawMessage(v)
		}
	}
	return e
}

// renameClient sends the rename, then reads it back. Only when the read-back
// shows a different name is the old one put back; if the read-back itself
// fails the outcome is unknown and nothing more is sent, so a rename that
// worked is never undone by mistake.
//
// The flag user_set_name_type is write-only (CLIENT_LIST_GET does not
// report it), so an undo cannot restore its earlier value; it sends true,
// as the app does for every rename.
func (c *Client) renameClient(ctx context.Context, mac, name string, userSet bool) error {
	if err := ValidateClientName(name); err != nil {
		return err
	}
	cur, err := c.ClientEntry(ctx, mac)
	if err != nil {
		return err
	}
	oldName := b64Text(cur.Str("name"))
	// Once sent, the write runs to completion even if the caller gives up.
	wctx := context.WithoutCancel(ctx)
	if _, err := c.request(wctx, tmp.OpClientListSet, map[string]any{"client_list": []any{renameEntry(cur, name, userSet)}}); err != nil {
		// The frame may have been applied before the error (a timeout):
		// the outcome is unknown, not refused.
		return fmt.Errorf("%w: the Deco did not confirm the rename (it may have taken it): %w", deco.ErrNotApplied, err)
	}
	after, err := c.ClientEntry(wctx, mac)
	if err != nil {
		return fmt.Errorf("%w: the rename was sent, but reading it back failed: %w", deco.ErrNotApplied, err)
	}
	if b64Text(after.Str("name")) == name {
		return nil
	}
	undo := map[string]any{"client_list": []any{renameEntry(cur, oldName, true)}}
	if _, rerr := c.request(wctx, tmp.OpClientListSet, undo); rerr != nil {
		return fmt.Errorf("%w: the Deco accepted the new name but does not show it, and restoring the old name failed: %w", deco.ErrNotApplied, rerr)
	}
	return fmt.Errorf("%w: the Deco accepted the new name but does not show it; the old name was restored", deco.ErrNotApplied)
}
