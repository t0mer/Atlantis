package ata

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// echoRelay is a relay whose device echoes every received byte, uppercased,
// on the next TRANSFER response.
type echoRelay struct {
	mu        sync.Mutex
	methods   []string
	lastSN    int64
	pending   []byte
	tokens    []string
	errCode   int
	badSN     bool
	connected bool
}

func (r *echoRelay) Token() string { return "acct" }

func (r *echoRelay) Passthrough(_ context.Context, url, dev string, req any) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := req.(map[string]any)
	method := m["method"].(string)
	r.methods = append(r.methods, method)
	r.tokens = append(r.tokens, m["token"].(string))
	if url != "https://relay.example" || dev != "dev-1" {
		return nil, errors.New("wrong addressing")
	}
	out := map[string]any{"err_code": r.errCode}
	switch method {
	case "CONNECT":
		if m["type"] != "TMP" {
			return nil, errors.New("CONNECT without type TMP")
		}
		r.connected = true
	case "TRANSFER":
		if !r.connected {
			return nil, errors.New("TRANSFER before CONNECT")
		}
		sn := m["sn"].(int64)
		if sn != r.lastSN+1 {
			r.badSN = true
		}
		r.lastSN = sn
		in, _ := base64.StdEncoding.DecodeString(m["raw_data"].(string))
		reply := r.pending
		r.pending = bytes.ToUpper(in)
		out["sn"] = sn
		out["raw_data"] = base64.StdEncoding.EncodeToString(reply)
	}
	b, _ := json.Marshal(out)
	return b, nil
}

func dial(t *testing.T, r *echoRelay) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), r, "https://relay.example", "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEchoRoundTrip(t *testing.T) {
	r := &echoRelay{}
	c := dial(t, r)
	if _, err := c.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("world"))
	got := make([]byte, 11)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "HELLO WORLD" {
		t.Fatalf("got %q", got)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.methods[0] != "CONNECT" || r.methods[len(r.methods)-1] != "DISCONNECT" {
		t.Errorf("lifecycle wrong: %v", r.methods)
	}
	if r.badSN {
		t.Error("sn was not strictly increasing from 1")
	}
	for _, tok := range r.tokens {
		if !strings.HasPrefix(tok, "acct-") || tok != r.tokens[0] {
			t.Errorf("relay token wrong or unstable: %v", r.tokens)
			break
		}
	}
}

func TestLargeWriteIsBatched(t *testing.T) {
	r := &echoRelay{}
	c := dial(t, r)
	defer c.Close()
	big := bytes.Repeat([]byte("a"), 3*maxBatchBase64) // several batches
	c.Write(big)
	got := make([]byte, len(big))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.ToUpper(big)) {
		t.Fatal("data corrupted across batches")
	}
}

func TestRelayErrorEndsConnection(t *testing.T) {
	r := &echoRelay{}
	c := dial(t, r)
	r.mu.Lock()
	r.errCode = 5
	r.mu.Unlock()
	c.Write([]byte("x"))
	done := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "token expired") {
			t.Fatalf("want relay token error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after a relay error")
	}
	if _, err := c.Write([]byte("y")); err == nil {
		t.Fatal("Write after failure should error")
	}
}

func TestCloseUnblocksRead(t *testing.T) {
	c := dial(t, &echoRelay{})
	done := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}
