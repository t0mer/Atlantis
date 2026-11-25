package tmpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
)

func m5Clients(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/m5/client_list_get.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(b, &v)
	return v
}

func sentClient(t *testing.T, d *fakedeco.Device) map[string]any {
	t.Helper()
	var sent struct {
		Params struct {
			ClientList []map[string]any `json:"client_list"`
		} `json:"params"`
	}
	json.Unmarshal(d.Request(tmp.OpClientListSet), &sent)
	if len(sent.Params.ClientList) != 1 {
		t.Fatalf("one client entry expected: %s", d.Request(tmp.OpClientListSet))
	}
	return sent.Params.ClientList[0]
}

func TestRenameClientIsGatedUntilVerified(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpClientListGet, m5Clients(t))
	c := newClient(t, d)
	if c.CanRenameClients() {
		t.Skip("renaming is verified; the gate test no longer applies")
	}
	if err := c.RenameClient(context.Background(), "02:00:00:00:00:02", "Kitchen tablet"); !errors.Is(err, deco.ErrNotVerified) {
		t.Fatalf("got %v", err)
	}
	if d.Request(tmp.OpClientListSet) != nil {
		t.Fatal("an unverified rename was sent")
	}
}

// The entry matches the app's rename screen: the MAC as listed, the name
// in base64, the user flag, and only the copied fields the device reports.
func TestProbeRenameClientSendsTheAppsEntry(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpClientListGet, m5Clients(t))
	c := newClient(t, d)
	if err := c.ProbeRenameClient(context.Background(), "02:00:00:00:00:02", "מטבח", true); err != nil {
		t.Fatal(err)
	}
	e := sentClient(t, d)
	want := map[string]any{"mac": "02-00-00-00-00-02", "name": b64("מטבח"), "user_set_name_type": true,
		"client_type": "tablet", "enable_priority": false, "remain_time": float64(0), "space_id": "1"}
	if len(e) != len(want) {
		t.Fatalf("fields %v", e)
	}
	for k, v := range want {
		if e[k] != v {
			t.Errorf("%s = %v, want %v", k, e[k], v)
		}
	}
	got, _ := c.ClientEntry(context.Background(), "02-00-00-00-00-02")
	if got.Str("name") != b64("מטבח") {
		t.Fatal("the Deco does not show the new name")
	}
}

func TestRenameUnknownClientSendsNothing(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpClientListGet, m5Clients(t))
	c := newClient(t, d)
	if err := c.ProbeRenameClient(context.Background(), "02:00:00:00:00:99", "x", true); !errors.Is(err, deco.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	for _, bad := range []string{"", string(make([]byte, 65)), "a\nb"} {
		if err := c.ProbeRenameClient(context.Background(), "02:00:00:00:00:02", bad, true); !errors.Is(err, deco.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if d.Request(tmp.OpClientListSet) != nil {
		t.Fatal("a rename was sent")
	}
}

func TestRenameNotShownIsUndone(t *testing.T) {
	d := fakedeco.New()
	d.SetResult(tmp.OpClientListGet, m5Clients(t))
	d.IgnoreClientSets(1)
	c := newClient(t, d)
	err := c.ProbeRenameClient(context.Background(), "02:00:00:00:00:02", "New name", true)
	if err == nil || sentClient(t, d)["name"] != b64("name-2") {
		t.Fatalf("the old name must be sent back: %v %v", err, sentClient(t, d)["name"])
	}
}
