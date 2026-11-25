package luci

import (
	"context"
	"strings"
	"testing"
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
