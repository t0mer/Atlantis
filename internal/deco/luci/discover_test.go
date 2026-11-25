package luci

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/t0mer/atlantis/internal/deco/luci/fakedeco"
)

func TestDiscoverFormsCrawlsWebUI(t *testing.T) {
	f := newFake(t)
	c, err := New(f.Plain.URL, "secret", Options{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.DiscoverForms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fetched != 3 {
		t.Fatalf("fetched %d files, want 3 (index, app.js, nested wifi.js)", rep.Fetched)
	}
	byKey := map[string]FormRef{}
	for _, f := range rep.Forms {
		byKey[f.Area+"/"+f.Form] = f
	}
	dl, ok := byKey["device/device_list"]
	if !ok || !contains(dl.Operations, "read") {
		t.Fatalf("device/device_list missing or without read op: %+v", rep.Forms)
	}
	if wl, ok := byKey["wireless/wlan"]; !ok || !contains(wl.Operations, "write") {
		t.Fatalf("nested-script form wireless/wlan not found: %+v", rep.Forms)
	}
	if _, ok := byKey["?/led"]; !ok {
		t.Fatalf("area-less form led not found: %+v", rep.Forms)
	}
	if _, ok := byKey["?/device_list"]; ok {
		t.Fatal("a form with an area was also reported as area-less")
	}

	md := rep.Markdown("Deco X20", "1.2.3", map[string]string{"device/device_list": "ok"})
	for _, want := range []string{"Deco X20", "`wireless` | `wlan`", "`device/device_list` | ok"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestResolveSameHostRejectsOffHost(t *testing.T) {
	c, _ := New("http://192.168.68.1", "pw", Options{})
	cases := map[string]bool{
		"js/app.js":                        true,
		"/webpages/x.js":                   true,
		"http://192.168.68.1/a.js":         true,
		"https://cdn.example.com/a.js":     false,
		"javascript:alert(1)":              false,
		"data:text/javascript,alert(1).js": false,
	}
	for ref, want := range cases {
		if _, ok := c.resolveSameHost("/webpages/index.html", ref); ok != want {
			t.Errorf("resolveSameHost(%q) ok = %v, want %v", ref, ok, want)
		}
	}
}

func TestDiscoverCountsFailedFetchesTowardBound(t *testing.T) {
	f := newFake(t)
	var refs strings.Builder
	for i := 0; i < maxDiscoverFiles*2; i++ {
		fmt.Fprintf(&refs, "load(\"missing%d.js\");\n", i)
	}
	f.Update(func(f *fakedeco.Fake) { f.SetPage("/webpages/index.html", "<script>"+refs.String()+"</script>") })
	c, _ := New(f.Plain.URL, "secret", Options{})
	rep, err := c.DiscoverForms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if total := rep.Fetched + rep.Failed; total > maxDiscoverFiles {
		t.Fatalf("crawl made %d attempts, bound is %d", total, maxDiscoverFiles)
	}
	if len(rep.FailSamples) == 0 || len(rep.FailSamples) > maxFailSamples {
		t.Fatalf("fail samples = %d, want 1..%d", len(rep.FailSamples), maxFailSamples)
	}
}

func TestDiscoverDoesNotMoveAPIBaseOnWebRedirect(t *testing.T) {
	f := newFake(t)
	f.Update(func(f *fakedeco.Fake) { f.SetRootRedirect("https://127.0.0.1:4443/webpages/index.html") })
	c, _ := New(f.Plain.URL, "secret", Options{})
	before := c.BaseURL()
	if _, err := c.DiscoverForms(context.Background()); err == nil {
		t.Fatal("expected an error when the web UI redirects to another scheme")
	}
	if after := c.BaseURL(); after != before {
		t.Fatalf("base moved from %s to %s", before, after)
	}
}

func TestMarkdownEscapesDeviceStrings(t *testing.T) {
	r := &FormsReport{Forms: []FormRef{{Area: "device", Form: "device_list"}}}
	md := r.Markdown("Evil | model\n# injected", "1.0`x`", nil)
	firstLine, _, _ := strings.Cut(md, "\n")
	if strings.Contains(md, "\n# injected") || !strings.Contains(firstLine, `\|`) || strings.Contains(firstLine, "`") {
		t.Fatalf("device strings not escaped:\n%s", md)
	}
}
