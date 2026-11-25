package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

func TestSeedDecoNeverOverridesTheUI(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, ":memory:")
	defer st.Close()
	box, _ := secret.New(secret.NewKey())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Deco: config.Deco{Host: "192.0.2.1", Transport: "ssh", Email: "a@example.com", Password: "first"}}

	if err := seedDeco(ctx, cfg, st, box, log); err != nil {
		t.Fatal(err)
	}
	n, ok, _ := st.GetNetwork(ctx)
	if p, _ := box.Open(n.PasswordEnc); !ok || n.Host != "192.0.2.1" || p != "first" {
		t.Fatalf("first start must store the configured Deco: %+v", n)
	}

	// Changed in the UI: a new address, its own key and password.
	n.Host, n.HostKey = "192.0.2.2", "SHA256:other"
	n.PasswordEnc, _ = box.Seal("from-ui")
	st.SaveNetwork(ctx, n)
	cfg.Deco.Password = "stale"
	if err := seedDeco(ctx, cfg, st, box, log); err != nil {
		t.Fatal(err)
	}
	got, _, _ := st.GetNetwork(ctx)
	if p, _ := box.Open(got.PasswordEnc); got.Host != "192.0.2.2" || got.HostKey != "SHA256:other" || p != "from-ui" {
		t.Fatalf("a restart must not undo the UI: %+v", got)
	}

	// Stored without a password: the configured one fills it in.
	got.Host, got.PasswordEnc = "192.0.2.1", ""
	st.SaveNetwork(ctx, got)
	seedDeco(ctx, cfg, st, box, log)
	got, _, _ = st.GetNetwork(ctx)
	if p, _ := box.Open(got.PasswordEnc); p != "stale" {
		t.Fatal("a missing password must be filled from the configuration")
	}
}
