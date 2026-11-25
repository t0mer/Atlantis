package cloud_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/cloud"
	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
	"github.com/t0mer/atlantis/internal/tplinkcloud"
	"github.com/t0mer/atlantis/internal/tplinkcloud/fakecloud"
)

func setup(t *testing.T) (*cloud.Client, *fakedeco.Device, *fakecloud.Fake) {
	t.Helper()
	fc := fakecloud.New()
	t.Cleanup(fc.Close)
	dev := fakedeco.New()
	fc.SetPassthrough(dev.Relay)
	fc.SetDevices([]fakecloud.Device{{ID: "master-1", Master: true, Online: true, GroupID: "g1", GroupName: "Home"}})

	acct, err := tplinkcloud.New(tplinkcloud.Options{
		Keys:         tplinkcloud.AppKeys{AccessKey: fakecloud.AccessKey, Secret: fakecloud.Secret},
		TerminalUUID: tplinkcloud.NewTerminalUUID(), BootstrapURL: fc.URL, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := acct.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	sites, err := acct.Sites(ctx)
	if err != nil || len(sites) != 1 {
		t.Fatalf("sites: %v %v", sites, err)
	}
	return cloud.New(acct, sites[0]), dev, fc
}

func TestRawThroughRelay(t *testing.T) {
	c, dev, _ := setup(t)
	ctx := context.Background()
	res, err := c.Raw(ctx, cloud.RawArea, "DEVICE_LIST_GET")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), `"device_id":"dev-1"`) {
		t.Fatalf("result: %s", res)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if !dev.Freed() {
		t.Error("session token not freed on logout")
	}
}

func TestRawRedactsWifiPasswords(t *testing.T) {
	c, dev, _ := setup(t)
	defer c.Logout(context.Background())
	dev.SetResult(tmp.OpWirelessGet, map[string]any{"band2_4": map[string]any{
		"host": map[string]any{"enable": true, "ssid": "SG9tZQ==", "password": "c2VjcmV0LXdpZmk="},
	}})
	res, err := c.Raw(context.Background(), cloud.RawArea, "WIRELESS_GET")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res), "c2VjcmV0LXdpZmk=") || !strings.Contains(string(res), "[redacted]") {
		t.Fatalf("Wi-Fi password not redacted: %s", res)
	}
}

func TestRawRefusesUnknownAndWriteOpcodes(t *testing.T) {
	c, _, _ := setup(t)
	defer c.Logout(context.Background())
	ctx := context.Background()
	if _, err := c.Raw(ctx, cloud.RawArea, "DEVICE_LIST_REBOOT"); err == nil {
		t.Fatal("unknown/write opcode accepted")
	}
	if _, err := c.Raw(ctx, "device", "device_list"); err == nil {
		t.Fatal("luci area accepted")
	}
	var na tmp.ErrOpcodeNotAllowed
	if _, err := c.Request(ctx, 0x4016, nil); !errors.As(err, &na) {
		t.Fatalf("want ErrOpcodeNotAllowed, got %v", err)
	}
}

func TestTypedReadsNotImplementedYet(t *testing.T) {
	c, _, _ := setup(t)
	if _, err := c.Devices(context.Background()); !errors.Is(err, cloud.ErrNotImplemented) {
		t.Fatalf("got %v", err)
	}
}
