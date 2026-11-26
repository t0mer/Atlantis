package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/notify"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

func TestOpenBoxUsesAKeyFileNextToTheData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := &config.Config{StorePath: filepath.Join(dir, "atlantis.db")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, cfg.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	box, _, err := openBox(ctx, cfg, st, log)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := box.Seal("decopass")
	st.SaveNetwork(ctx, store.Network{Host: "h", Transport: "ssh", PasswordEnc: sealed})

	// A restart reads the same key.
	box2, _, err := openBox(ctx, cfg, st, log)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := box2.Open(sealed); err != nil || p != "decopass" {
		t.Fatal("the key file must be reused", err)
	}

	// Encrypted data with the key file gone: refuse, never make a new key.
	os.Remove(filepath.Join(dir, secret.KeyFileName))
	if _, _, err := openBox(ctx, cfg, st, log); err == nil || !strings.Contains(err.Error(), "ATLANTIS_SECRET_KEY") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, secret.KeyFileName)); err == nil {
		t.Fatal("a new key was created for a database with encrypted data")
	}

	// A key that does not open the stored passwords is refused.
	cfg.SecretKey = secret.NewKey()
	if _, _, err := openBox(ctx, cfg, st, log); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a wrong key must be refused: %v", err)
	}
}

// The OpenWA defaults earlier versions saved move into the channels that
// relied on them, and the saved row goes.
func TestMoveOpenWADefaults(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.New(secret.NewKey())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	disp := notify.New(st, box, func() string { return "Home" }, log)
	key, _ := box.Seal("k")
	st.SetSetting(ctx, "openwa", map[string]string{"url": "http://wa.lan", "default_chat": "group", "api_key": key})
	empty, _ := disp.SealConfig(notify.Config{})
	st.SaveChannel(ctx, store.Channel{Kind: notify.KindOpenWA, Name: "wa", ConfigEnc: empty})
	t.Setenv("ATLANTIS_OPENWA_URL", "http://ignored-when-saved")
	if err := moveOpenWADefaults(ctx, st, box, disp, log); err != nil {
		t.Fatal(err)
	}
	chs, _ := st.Channels(ctx)
	if c, _ := disp.OpenConfig(chs[0]); c != (notify.Config{URL: "http://wa.lan", Chat: "group", APIKey: "k"}) {
		t.Fatalf("%+v", c)
	}
	if have, _ := st.GetSetting(ctx, "openwa", new(map[string]string)); have {
		t.Fatal("the saved defaults must be removed")
	}
	// It runs once: a key cleared on the channel later is not put back.
	t.Setenv("ATLANTIS_OPENWA_API_KEY", "old-key")
	cleared, _ := disp.SealConfig(notify.Config{URL: "http://wa.lan", Chat: "group"})
	chs[0].ConfigEnc = cleared
	st.SaveChannel(ctx, chs[0])
	if err := moveOpenWADefaults(ctx, st, box, disp, log); err != nil {
		t.Fatal(err)
	}
	chs, _ = st.Channels(ctx)
	if c, _ := disp.OpenConfig(chs[0]); c.APIKey != "" {
		t.Fatal("the move must not run again")
	}
}

// Flags earlier versions had are accepted and ignored, so old command lines
// still start.
func TestRemovedFlagsAreIgnored(t *testing.T) {
	cmd := newServeCmd()
	if err := cmd.ParseFlags([]string{"--config", "/data/atlantis.yaml", "--log-level", "debug", "--server-listen", ":9"}); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	warnRemovedFlags(cmd, slog.New(slog.NewTextHandler(&b, nil)))
	if !strings.Contains(b.String(), "--config is no longer used") || !strings.Contains(b.String(), "--log-level") || strings.Contains(b.String(), "server-listen") {
		t.Fatal(b.String())
	}
	if err := newHealthcheckCmd().ParseFlags([]string{"--config", "x"}); err != nil {
		t.Fatal(err)
	}
}
