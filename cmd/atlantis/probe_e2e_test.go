package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/luci/fakedeco"
)

func newFake(t *testing.T) *fakedeco.Fake {
	t.Helper()
	f, err := fakedeco.New("secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

func TestProbeAgainstFakeDeco(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	if err := runProbe(ctx, probeOptions{host: f.Plain.URL, password: "secret"}); err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if err := runProbe(ctx, probeOptions{host: f.Plain.URL, password: "wrong"}); err == nil {
		t.Fatal("expected failure with wrong password")
	}
}

func TestProbeDumpAndDiscover(t *testing.T) {
	f := newFake(t)
	dumpDir, outDir := t.TempDir(), t.TempDir()
	err := runProbe(context.Background(), probeOptions{
		host: f.Plain.URL, password: "secret",
		dump: true, dumpDir: dumpDir,
		discover: true, outDir: outDir,
	})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(dumpDir, "*.json"))
	if len(files) != 5 {
		t.Fatalf("got %d fixtures, want 5: %v", len(files), files)
	}
	for _, p := range files {
		b, _ := os.ReadFile(p)
		for _, leaked := range []string{"AA-BB-CC-DD-EE-01", "11-22-33-44-55-66", "192.168.68.20", "must-not-leak"} {
			if strings.Contains(string(b), leaked) {
				t.Errorf("%s leaks %q", filepath.Base(p), leaked)
			}
		}
	}

	doc := filepath.Join(outDir, "device-forms-deco-x20-1-2-3.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("forms doc not written: %v", err)
	}
	for _, want := range []string{"`device/device_list` | ok", "`wireless` | `wlan`"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("forms doc missing %q", want)
		}
	}
}

func TestSlug(t *testing.T) {
	if got := slug("1.1.2 Build 20230307 Rel. 61473"); got != "1-1-2-build-20230307-rel-61473" {
		t.Fatalf("slug = %q", got)
	}
	if got := slug("  "); got != "unknown" {
		t.Fatalf("slug of blank = %q", got)
	}
}
