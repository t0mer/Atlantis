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
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

func TestOpenBoxUsesAKeyFileNextToTheData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := &config.Config{Store: config.Store{Path: filepath.Join(dir, "atlantis.db")}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, cfg.Store.Path)
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

	// The environment wins.
	cfg.SecretKey = secret.NewKey()
	if _, _, err := openBox(ctx, cfg, st, log); err != nil {
		t.Fatal(err)
	}
}
