package tmp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
)

func open(t *testing.T, d *fakedeco.Device) *tmp.Session {
	t.Helper()
	client, server := net.Pipe()
	go d.Serve(server)
	s, neg, err := tmp.Open(context.Background(), client, tmp.Options{Username: "owner@example.com", Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(neg), "Router") {
		t.Fatalf("negotiation result: %s", neg)
	}
	return s
}

func TestSessionLifecycle(t *testing.T) {
	d := fakedeco.New()
	s := open(t, d)
	res, err := s.Request(context.Background(), tmp.OpDeviceListGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), `"device_id":"dev-1"`) {
		t.Fatalf("result: %s", res)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !d.Freed() {
		t.Error("TOKEN_FREE with the allocated token was not sent")
	}
	sum := sha256.Sum256([]byte("owner@example.com"))
	neg := string(d.Request(tmp.OpCompNegotiate))
	if !strings.Contains(neg, `"role":"ROLE_OWNER"`) || !strings.Contains(neg, hex.EncodeToString(sum[:])) ||
		strings.Contains(neg, "owner@example.com") {
		t.Errorf("COMP_NEGOTIATE params wrong: %s", neg)
	}
	if !strings.HasPrefix(string(d.Request(tmp.OpDeviceListGet)), `{"config_version":`) {
		t.Errorf("request body wrong: %s", d.Request(tmp.OpDeviceListGet))
	}
}

func TestLargeReplyIsPulledInFragments(t *testing.T) {
	d := fakedeco.New()
	big := strings.Repeat("x", 3*tmp.MaxFragment)
	d.SetResult(tmp.OpClientListGet, map[string]any{"blob": big})
	s := open(t, d)
	defer s.Close()
	res, err := s.Request(context.Background(), tmp.OpClientListGet, map[string]string{"device_mac": "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), big) {
		t.Fatal("large reply truncated")
	}
}

func TestTokenExpiryReallocatesOnce(t *testing.T) {
	d := fakedeco.New()
	s := open(t, d)
	defer s.Close()
	d.ExpireTokenOnce()
	if _, err := s.Request(context.Background(), tmp.OpDeviceListGet, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDisallowedOpcodeNeverSent(t *testing.T) {
	d := fakedeco.New()
	s := open(t, d)
	defer s.Close()
	for _, op := range []tmp.Opcode{0x4016, 0x4008, tmp.OpTokenFree} {
		_, err := s.Request(context.Background(), op, nil)
		var na tmp.ErrOpcodeNotAllowed
		if !errors.As(err, &na) {
			t.Errorf("%s: want ErrOpcodeNotAllowed, got %v", op, err)
		}
	}
	for _, op := range d.Seen() {
		if op == 0x4016 || op == 0x4008 {
			t.Fatalf("%s reached the device", op)
		}
	}
}

func TestUnsupportedOpcodeReturnsResultError(t *testing.T) {
	s := open(t, fakedeco.New())
	defer s.Close()
	_, err := s.Request(context.Background(), tmp.OpFWLatestGet, nil)
	var re tmp.ResultError
	if !errors.As(err, &re) || re.Code != -40101 {
		t.Fatalf("want ResultError -40101, got %v", err)
	}
}

func TestByeIsReported(t *testing.T) {
	d := fakedeco.New()
	d.ByeAfter(tmp.OpDeviceListGet)
	s := open(t, d)
	defer s.Close()
	if _, err := s.Request(context.Background(), tmp.OpDeviceListGet, nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.Request(context.Background(), tmp.OpDeviceListGet, nil)
	var bye tmp.ByeError
	if !errors.As(err, &bye) || !strings.Contains(err.Error(), "client limit") {
		t.Fatalf("want client-limit bye, got %v", err)
	}
}
