package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn/fakessh"
	"github.com/t0mer/atlantis/internal/secret"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// serve end to end: fake SSH Deco -> poll -> store -> API.
func TestServeEndToEnd(t *testing.T) {
	srv, err := fakessh.New("owner@example.com", "decopass")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, sshPort := srv.Addr()
	port := freePort(t)
	var logs strings.Builder
	cfg := &config.Config{
		Server:    config.Server{Listen: "127.0.0.1:" + strconv.Itoa(port)},
		Deco:      config.Deco{Transport: "ssh", Host: host, Port: sshPort, Email: "Owner@Example.com", Password: "decopass"},
		Poll:      config.Poll{Fast: time.Minute, Slow: time.Minute, OfflineAfterMisses: 2},
		Store:     config.Store{Path: filepath.Join(t.TempDir(), "a.db"), RawRetention: time.Hour, RollupRetention: 2 * time.Hour},
		Metrics:   config.Metrics{Enabled: true, Path: "/metrics", PerClient: "watched"},
		Notify:    config.Notify{Timezone: "UTC"},
		Log:       config.Log{Level: "info", Format: "json"},
		SecretKey: secret.NewKey(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, newLogger(cfg.Log, &logs)) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if strings.Contains(logs.String(), "decopass") {
			t.Error("the Deco password reached the logs")
		}
	}()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if resp, err := cl.Get(base + "/health"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := cl.Post(base+"/api/v1/auth/setup", "application/json", strings.NewReader(`{"username":"admin","password":"long enough pw"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("setup: %v %v", err, resp)
	}
	resp.Body.Close()
	var nodes []map[string]any
	for time.Now().Before(deadline) {
		resp, err := cl.Get(base + "/api/v1/nodes")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		json.Unmarshal(b, &nodes)
		if len(nodes) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(nodes) != 1 || nodes[0]["Nickname"] != "Living" {
		t.Fatalf("nodes: %v\nlogs:\n%s", nodes, logs.String())
	}
	resp, _ = cl.Get(base + "/api/v1/session")
	var st map[string]any
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st["state"] != "connected" {
		t.Fatalf("session: %v", st)
	}
	resp, _ = cl.Get(base + "/")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Atlantis") {
		t.Fatal("UI placeholder not served")
	}
	if !strings.Contains(logs.String(), "ssh host key") {
		t.Fatal("host key not logged on first use")
	}
}

func TestLoggerRedactsSecrets(t *testing.T) {
	var b strings.Builder
	l := newLogger(config.Log{Level: "info", Format: "json"}, &b)
	l.Info("x", "password", "hunter2", "api_key", "k", "token", "t", "host", "192.0.2.1")
	out := b.String()
	if strings.Contains(out, "hunter2") || strings.Contains(out, `"k"`) || !strings.Contains(out, "192.0.2.1") {
		t.Fatal(out)
	}
}
