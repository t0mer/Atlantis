package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/tmp"
)

func TestClientRenameProbeRenamesAndRestores(t *testing.T) {
	probeSettle = 0
	c, d := probeClient(t, nil)
	d.SetResult(tmp.OpClientListGet, map[string]any{"client_list": []any{
		map[string]any{"mac": "02-00-00-00-00-02", "name": enc("Living room TV"), "client_type": "tv", "enable_priority": false, "remain_time": 0, "space_id": "1"},
	}})
	dir := t.TempDir()
	var out bytes.Buffer
	if err := clientRenameProbe(context.Background(), c, "02:00:00:00:00:02", nil, dir, sshProbeOptions{out: &out, errOut: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	e, _ := c.ClientEntry(context.Background(), "02-00-00-00-00-02")
	if b64Plain(e.Str("name")) != "Living room TV" {
		t.Fatal("the original name is not back")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "client_rename_probe.json"))
	if !bytes.Contains(b, []byte(`"pass"`)) || !bytes.Contains(b, []byte("hebrew")) || bytes.Contains(b, []byte("Living")) || bytes.Contains(b, []byte("02-00")) {
		t.Fatalf("record:\n%s", b)
	}
}

func TestClientRenameProbeChecksTheRestoreFirst(t *testing.T) {
	c, d := probeClient(t, nil)
	d.SetResult(tmp.OpClientListGet, map[string]any{"client_list": []any{
		map[string]any{"mac": "02-00-00-00-00-03", "name": enc(strings.Repeat("x", 70))},
	}})
	var out bytes.Buffer
	err := clientRenameProbe(context.Background(), c, "02:00:00:00:00:03", nil, t.TempDir(), sshProbeOptions{out: &out, errOut: &out})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") || d.Request(tmp.OpClientListSet) != nil {
		t.Fatalf("got %v", err)
	}
}
