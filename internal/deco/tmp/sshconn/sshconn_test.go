package sshconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
)

// fakeSSH is an SSH server that accepts one user/password and forwards
// direct-tcpip channels for 127.0.0.1:20002 to a fake Deco.
type fakeSSH struct {
	ln       net.Listener
	key      ssh.Signer
	dev      *fakedeco.Device
	mu       sync.Mutex
	attempts int
	targets  []string
}

func newFakeSSH(t *testing.T, user, password string) *fakeSSH {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSSH{ln: ln, key: signer, dev: fakedeco.New()}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			f.mu.Lock()
			f.attempts++
			f.mu.Unlock()
			if c.User() == user && string(p) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(nc, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSSH) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if nch.ChannelType() != "direct-tcpip" || ssh.Unmarshal(nch.ExtraData(), &p) != nil {
			nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		f.mu.Lock()
		f.targets = append(f.targets, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
		f.mu.Unlock()
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { f.dev.Serve(ch); ch.Close() }()
	}
}

func (f *fakeSSH) opts(user, password string) Options {
	host, port, _ := net.SplitHostPort(f.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return Options{Host: host, Port: p, User: user, Password: password}
}

func TestSessionOverSSH(t *testing.T) {
	user := Username("Owner@Example.com", true, false)
	f := newFakeSSH(t, user, "s3cret")
	var pinned string
	o := f.opts(user, "s3cret")
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
	if !f.dev.Freed() {
		t.Error("token not freed")
	}
	if pinned != Fingerprint(f.key.PublicKey()) {
		t.Errorf("TOFU fingerprint not reported: %q", pinned)
	}
	if len(f.targets) != 1 || f.targets[0] != "127.0.0.1:20002" {
		t.Errorf("channel target: %v", f.targets)
	}

	// The pinned key is accepted on the next connection.
	o.HostKeyPin, o.OnHostKey = pinned, nil
	conn, err = Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestWrongPasswordIsOneAttemptAndNotLeaked(t *testing.T) {
	f := newFakeSSH(t, "u", "right")
	_, err := Dial(context.Background(), f.opts("u", "wrong-pass-123"))
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-pass-123") {
		t.Fatalf("password leaked: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempts != 1 {
		t.Fatalf("want exactly one password attempt, got %d", f.attempts)
	}
}

func TestHostKeyPinMismatch(t *testing.T) {
	f := newFakeSSH(t, "u", "p")
	o := f.opts("u", "p")
	o.HostKeyPin = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := Dial(context.Background(), o); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("want ErrHostKeyChanged, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempts != 0 {
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
