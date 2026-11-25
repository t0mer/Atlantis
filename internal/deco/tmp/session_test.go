package tmp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

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

func TestCloseAbortsWaitingRequest(t *testing.T) {
	d := fakedeco.New()
	d.Stall(tmp.OpClientListGet)
	s := open(t, d)
	errc := make(chan error, 1)
	go func() { _, err := s.Request(context.Background(), tmp.OpClientListGet, nil); errc <- err }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	s.Close()
	if err := <-errc; !errors.Is(err, tmp.ErrSessionClosed) {
		t.Fatalf("want ErrSessionClosed, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("Close waited for the stalled request")
	}
	if !d.Freed() {
		t.Error("token not freed after aborting the request")
	}
}

func TestFatalErrorIsStickyAndCloseReturns(t *testing.T) {
	client, server := net.Pipe()
	d := fakedeco.New()
	go d.Serve(server)
	s, _, err := tmp.Open(context.Background(), client, tmp.Options{Heartbeat: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	server.Close() // the stream dies under the session
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := s.Request(ctx, tmp.OpDeviceListGet, nil)
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("request %d: want the stream error, got %v", i, err)
		}
	}
	if s.Err() == nil {
		t.Fatal("Err() should report the fatal error")
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung after a fatal error")
	}
}

func TestAssocRefuseIsFatal(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		buf := make([]byte, 64)
		server.Read(buf)
		server.Write([]byte{1, 1, tmp.CtrlAssocRefuse, 0})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := tmp.Open(ctx, client, tmp.Options{})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestAssociateOption(t *testing.T) {
	for _, assoc := range []bool{false, true} {
		client, server := net.Pipe()
		d := fakedeco.New()
		go d.Serve(server)
		s, _, err := tmp.Open(context.Background(), client, tmp.Options{Associate: assoc})
		if err != nil {
			t.Fatalf("associate=%v: %v", assoc, err)
		}
		if _, err := s.Request(context.Background(), tmp.OpDeviceListGet, nil); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if want := map[bool]int{false: 0, true: 1}[assoc]; d.Associations() != want {
			t.Errorf("associate=%v: %d association requests", assoc, d.Associations())
		}
	}
}
