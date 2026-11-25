package sshconn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn/fakessh"
)

func newFake(t *testing.T, user, password string) *fakessh.Server {
	t.Helper()
	f, err := fakessh.New(user, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func opts(f *fakessh.Server, user, password string) Options {
	host, port := f.Addr()
	return Options{Host: host, Port: port, User: user, Password: password}
}

func TestSessionOverSSH(t *testing.T) {
	user := Username("Owner@Example.com", true, false)
	f := newFake(t, user, "s3cret")
	var pinned string
	o := opts(f, user, "s3cret")
	o.AcceptNew = true
	o.OnHostKey = func(fp string) { pinned = fp }
	conn, err := Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := tmp.Open(context.Background(), conn, tmp.Options{Username: "owner@example.com", Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := sess.Request(context.Background(), tmp.OpDeviceListGet, nil)
	if err != nil || !strings.Contains(string(res), "dev-1") {
		t.Fatalf("%s %v", res, err)
	}
	sess.Close()
	if !f.Device.Freed() {
		t.Error("token not freed")
	}
	if pinned != Fingerprint(f.Key.PublicKey()) {
		t.Errorf("TOFU fingerprint not reported: %q", pinned)
	}
	if tg := f.Targets(); len(tg) != 1 || tg[0] != "127.0.0.1:20002" {
		t.Errorf("channel target: %v", tg)
	}

	// The pinned key is accepted on the next connection.
	o.HostKeyPin, o.OnHostKey, o.AcceptNew = pinned, nil, false
	conn, err = Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestWrongPasswordIsOneAttemptAndNotLeaked(t *testing.T) {
	f := newFake(t, "u", "right")
	o := opts(f, "u", "wrong-pass-123")
	o.AcceptNew = true
	_, err := Dial(context.Background(), o)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-pass-123") {
		t.Fatalf("password leaked: %v", err)
	}
	if n := f.Attempts(); n != 1 {
		t.Fatalf("want exactly one password attempt, got %d", n)
	}
}

func TestHostKeyPinMismatch(t *testing.T) {
	f := newFake(t, "u", "p")
	o := opts(f, "u", "p")
	o.HostKeyPin = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := Dial(context.Background(), o); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("want ErrHostKeyChanged, got %v", err)
	}
	if f.Attempts() != 0 {
		t.Fatal("password must not be sent to a host with the wrong key")
	}
}

func TestUsername(t *testing.T) {
	if got := Username(" Owner@Example.com ", false, false); got != "owner@example.com" {
		t.Fatal(got)
	}
	// sha1("owner@example.com"), computed with Python hashlib.
	if got := Username("Owner@Example.com", true, false); got != "66f171d88474476cb4933b33b39cceba825e24f1" {
		t.Fatal(got)
	}
	if Username("Owner@Example.com", true, true) == Username("Owner@Example.com", true, false) {
		t.Fatal("raw must hash the email as given")
	}
}

func TestUnknownHostKeyFailsClosed(t *testing.T) {
	f := newFake(t, "u", "p")
	var seen string
	o := opts(f, "u", "p")
	o.OnHostKey = func(fp string) { seen = fp }
	if _, err := Dial(context.Background(), o); !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("want ErrUnknownHostKey, got %v", err)
	}
	if f.Attempts() != 0 {
		t.Fatal("password sent to an unverified host")
	}
	if seen == "" {
		t.Fatal("fingerprint not reported")
	}
}
