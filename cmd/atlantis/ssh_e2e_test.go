package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn/fakessh"
)

func TestSSHProbeEndToEnd(t *testing.T) {
	user := sshconn.Username("owner@example.com", false, false)
	srv, err := fakessh.New(user, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()
	var out, errOut bytes.Buffer
	o := sshProbeOptions{
		host: host, port: port, email: "Owner@Example.com", password: "pw", skipTDP: true,
		dump: true, dumpDir: filepath.Join(t.TempDir(), "fx"), debug: true,
		in: strings.NewReader(""), out: &out, errOut: &errOut,
	}
	err = runSSHProbe(context.Background(), o)
	// Steps without canned replies fail; the session must still work and close.
	if err == nil || !strings.Contains(err.Error(), "FW_LATEST_GET") {
		t.Fatalf("got %v\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `"device_id": "dev-1"`) {
		t.Fatalf("no device list:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "SSH host key (pin with --host-key): SHA256:") {
		t.Errorf("host key not reported:\n%s", errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "pw\"") || srv.Attempts() != 1 {
		t.Errorf("password leak or extra attempts (%d)", srv.Attempts())
	}
	if srv.Device.Associations() != 1 || !srv.Device.Freed() {
		t.Errorf("associate=%d freed=%v", srv.Device.Associations(), srv.Device.Freed())
	}
}

func TestSSHProbeWrongPasswordStops(t *testing.T) {
	srv, _ := fakessh.New("owner@example.com", "right")
	defer srv.Close()
	host, port := srv.Addr()
	var out, errOut bytes.Buffer
	err := runSSHProbe(context.Background(), sshProbeOptions{
		host: host, port: port, email: "owner@example.com", password: "nope-nope", skipTDP: true,
		in: strings.NewReader(""), out: &out, errOut: &errOut,
	})
	if err == nil || !strings.Contains(err.Error(), "locks out") || strings.Contains(err.Error(), "nope-nope") {
		t.Fatalf("got %v", err)
	}
	if srv.Attempts() != 1 {
		t.Fatalf("want one attempt, got %d", srv.Attempts())
	}
}
