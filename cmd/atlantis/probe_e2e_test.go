package main

import (
	"context"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/luci/fakedeco"
)

func TestProbeAgainstFakeDeco(t *testing.T) {
	f, err := fakedeco.New("secret")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := runProbe(context.Background(), f.Plain.URL, "secret", ""); err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if err := runProbe(context.Background(), f.Plain.URL, "wrong", ""); err == nil {
		t.Fatal("expected failure with wrong password")
	}
}
