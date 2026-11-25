package tplinkcloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/tplinkcloud"
	"github.com/t0mer/atlantis/internal/tplinkcloud/fakecloud"
)

func newClient(t *testing.T, f *fakecloud.Fake) *tplinkcloud.Client {
	t.Helper()
	c, err := tplinkcloud.New(tplinkcloud.Options{
		Keys:         tplinkcloud.AppKeys{AccessKey: fakecloud.AccessKey, Secret: fakecloud.Secret},
		TerminalUUID: tplinkcloud.NewTerminalUUID(),
		BootstrapURL: f.URL,
		AllowHTTP:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRequiresKeys(t *testing.T) {
	if _, err := tplinkcloud.New(tplinkcloud.Options{TerminalUUID: "x"}); err == nil {
		t.Fatal("expected an error without app keys")
	}
}

func TestRefusesPlainHTTPByDefault(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	c, _ := tplinkcloud.New(tplinkcloud.Options{
		Keys:         tplinkcloud.AppKeys{AccessKey: fakecloud.AccessKey, Secret: fakecloud.Secret},
		TerminalUUID: "t", BootstrapURL: f.URL,
	})
	if err := c.Login(context.Background(), fakecloud.Email, fakecloud.Password); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("want non-https refusal, got %v", err)
	}
}

func TestLoginAndSites(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	f.SetDevices([]fakecloud.Device{
		{ID: "dev-home", MAC: "AABBCCDDEE01", Model: "X50", Name: "Living room", Online: true, Master: true,
			GroupID: "g-home", GroupName: "בית", Satellites: []fakecloud.Satellite{{ID: "dev-sat", MAC: "AA-BB-CC-DD-EE-02", Model: "X50", Nickname: "Office"}}},
		{ID: "dev-sat", MAC: "AABBCCDDEE02", Model: "X50", Online: true, GroupID: "g-home"},
		{ID: "dev-cabin", MAC: "AABBCCDDEE09", Model: "M4", Name: "Cabin", Master: true, GroupID: "g-cabin", GroupName: "Cabin"},
	})
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	sites, err := c.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 {
		t.Fatalf("want 2 sites (satellite folded into its master), got %+v", sites)
	}
	byID := map[string]tplinkcloud.Site{}
	for _, s := range sites {
		byID[s.ID] = s
	}
	home := byID["g-home"]
	if home.Name != "בית" || home.MasterDeviceID != "dev-home" || !home.Online || home.AppServerURL != f.URL {
		t.Errorf("home site wrong: %+v", home)
	}
	if len(home.Nodes) != 2 || home.Nodes[1].Name != "Office" || home.Nodes[1].MAC != "aa:bb:cc:dd:ee:02" || !home.Nodes[0].Master {
		t.Errorf("home nodes wrong: %+v", home.Nodes)
	}
	if byID["g-cabin"].Online {
		t.Errorf("cabin should be offline")
	}
}

func TestSitesPaginates(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	var devs []fakecloud.Device
	for i := range 650 {
		devs = append(devs, fakecloud.Device{ID: fmt.Sprintf("d%03d", i), Master: true, GroupID: fmt.Sprintf("g%03d", i)})
	}
	f.SetDevices(devs)
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	sites, err := c.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 650 {
		t.Fatalf("got %d sites, want 650", len(sites))
	}
}

func TestWrongPasswordIsAuthFailure(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	c := newClient(t, f)
	err := c.Login(context.Background(), fakecloud.Email, "nope")
	if !errors.Is(err, tplinkcloud.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "4 attempts left") {
		t.Errorf("remaining attempts not surfaced: %v", err)
	}
	if strings.Contains(err.Error(), "nope") {
		t.Errorf("password leaked into error: %v", err)
	}
}

func TestMFAFlow(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	f.SetRequireMFA(true)
	c := newClient(t, f)
	ctx := context.Background()

	err := c.Login(ctx, fakecloud.Email, fakecloud.Password)
	var mfa *tplinkcloud.MFAChallenge
	if !errors.As(err, &mfa) {
		t.Fatalf("want MFAChallenge, got %v", err)
	}
	if mfa.MaskedEmail != "o***@example.com" || len(mfa.Types) != 2 {
		t.Errorf("challenge wrong: %+v", mfa)
	}
	if err := c.SendMFACode(ctx, tplinkcloud.MFAEmail); err != nil || f.CodesSent() != 1 {
		t.Fatalf("send code: %v (sent %d)", err, f.CodesSent())
	}
	if err := c.VerifyMFA(ctx, tplinkcloud.MFAEmail, "000000"); !errors.Is(err, tplinkcloud.ErrAuthFailed) {
		t.Fatalf("wrong code should be an auth failure, got %v", err)
	}
	// The challenge stays open, so the user can type the code again.
	if err := c.VerifyMFA(ctx, tplinkcloud.MFAEmail, fakecloud.MFACode); err != nil {
		t.Fatal(err)
	}
	if c.Token() == "" {
		t.Fatal("no token after MFA")
	}
}

func TestExpiredTokenReloginsOnce(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	f.SetDevices([]fakecloud.Device{{ID: "d1", Master: true, GroupID: "g1"}})
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	f.ExpireTokens()
	if _, err := c.Sites(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Logins() != 2 {
		t.Fatalf("want exactly one re-login, got %d logins", f.Logins())
	}
}

func TestCallsBeforeLogin(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	c := newClient(t, f)
	if _, err := c.Sites(context.Background()); !errors.Is(err, tplinkcloud.ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
}

func TestPassthrough(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	var gotDevice string
	f.SetPassthrough(func(id string, rd json.RawMessage) (any, error) {
		gotDevice = id
		var in map[string]any
		json.Unmarshal(rd, &in)
		return map[string]any{"err_code": 0, "echo": in["method"]}, nil
	})
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	out, err := c.Passthrough(ctx, f.URL, "dev-1", map[string]string{"method": "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	if gotDevice != "dev-1" || !strings.Contains(string(out), `"echo":"CONNECT"`) {
		t.Fatalf("passthrough wrong: device=%s out=%s", gotDevice, out)
	}
}

func TestTransportErrorsDoNotLeakToken(t *testing.T) {
	f := fakecloud.New()
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	tok := c.Token()
	f.Close() // every further request fails at the transport
	_, err := c.Passthrough(ctx, f.URL, "dev-1", map[string]string{})
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("token leaked or no error: %v", err)
	}
}

func TestFailedReloginIsNotRepeated(t *testing.T) {
	f := fakecloud.New()
	defer f.Close()
	f.SetDevices([]fakecloud.Device{{ID: "d1", Master: true, GroupID: "g1"}})
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.Login(ctx, fakecloud.Email, fakecloud.Password); err != nil {
		t.Fatal(err)
	}
	f.SetRequireMFA(true) // the automatic re-login will need the user
	f.ExpireTokens()
	if _, err := c.Sites(ctx); err == nil {
		t.Fatal("re-login needing MFA should fail")
	}
	if _, err := c.Sites(ctx); !errors.Is(err, tplinkcloud.ErrNotLoggedIn) {
		t.Fatalf("second call must not sign in again, got %v", err)
	}
	n := 0
	for _, p := range f.Paths() {
		if p == "/api/v2/account/login" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 login requests (initial + one re-login), got %d", n)
	}
}
