package luci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco"
	"github.com/t0mer/atlantis/internal/deco/luci/fakedeco"
)

func newFake(t *testing.T) *fakedeco.Fake {
	t.Helper()
	f, err := fakedeco.New("secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

func TestLoginAndAdminCall(t *testing.T) {
	f := newFake(t)
	c, err := New(f.Plain.URL, "secret", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, err := c.call(ctx, "device", "device_list", "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		DeviceList []struct {
			Mac string `json:"mac"`
		} `json:"device_list"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || len(got.DeviceList) != 1 || got.DeviceList[0].Mac != "AA-BB-CC-DD-EE-01" {
		t.Fatalf("unexpected device_list: %s (%v)", raw, err)
	}
	if n := f.Logins(); n != 1 {
		t.Fatalf("logins = %d, want 1", n)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPasswordStopsRetries(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetWrongPassword(true) })
	c, _ := New(f.Plain.URL, "secret", Options{})
	ctx := context.Background()
	if err := c.Login(ctx); !errors.Is(err, deco.ErrWrongPassword) {
		t.Fatalf("first login err = %v, want ErrWrongPassword", err)
	}
	if err := c.Login(ctx); !errors.Is(err, deco.ErrAuthFailed) {
		t.Fatalf("second login err = %v, want ErrAuthFailed", err)
	}
}

func TestOtherAdminReturnsBusy(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetBusy(true) })
	c, _ := New(f.Plain.URL, "secret", Options{})
	if err := c.Login(context.Background()); !errors.Is(err, deco.ErrOtherAdmin) {
		t.Fatalf("err = %v, want ErrOtherAdmin", err)
	}
}

func TestExpiredSessionReloginsOnce(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetExpireAfter(1) })
	c, _ := New(f.Plain.URL, "secret", Options{})
	ctx := context.Background()
	if _, err := c.call(ctx, "device", "device_list", "read", nil); err != nil {
		t.Fatal(err)
	}
	// The first call invalidated the session; this one must re-login and succeed.
	if _, err := c.call(ctx, "device", "device_list", "read", nil); err != nil {
		t.Fatalf("call after expiry: %v", err)
	}
	if n := f.Logins(); n != 2 {
		t.Fatalf("logins = %d, want 2", n)
	}
}

func TestHTTPRedirectSwitchesToHTTPSAndPersists(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetRedirectToTLS(true) })
	var switched string
	c, _ := New(f.Plain.URL, "secret", Options{
		Pin:            f.TLSFingerprint(),
		OnSchemeChange: func(b string) { switched = b },
	})
	ctx := context.Background()
	if err := c.Login(ctx); err != nil {
		t.Fatalf("login via redirect: %v", err)
	}
	if !strings.HasPrefix(switched, "https://") || !strings.HasPrefix(c.BaseURL(), "https://") {
		t.Fatalf("scheme not switched: callback=%q base=%q", switched, c.BaseURL())
	}
}

func TestTOFUReportsFingerprint(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetRedirectToTLS(true) })
	var seen string
	c, _ := New(f.Plain.URL, "secret", Options{OnFingerprint: func(fp string) { seen = fp }})
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != f.TLSFingerprint() {
		t.Fatalf("fingerprint = %q, want %q", seen, f.TLSFingerprint())
	}
}

func TestPinMismatchIsRejected(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetRedirectToTLS(true) })
	c, _ := New(f.Plain.URL, "secret", Options{Pin: strings.Repeat("00", 32)})
	if err := c.Login(context.Background()); !errors.Is(err, deco.ErrCertChanged) {
		t.Fatalf("err = %v, want ErrCertChanged", err)
	}
}
