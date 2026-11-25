package api

import (
	"net/http"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDecoLifecycle(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	code, m, _ := e.do(t, "GET", "/api/v1/deco", "", false)
	if code != 200 || m["configured"] != true || m["running"] != true || m["host"] != "192.0.2.1" {
		t.Fatalf("%d %v", code, m)
	}
	if _, ok := m["password"]; ok {
		t.Fatal("the password must never be returned")
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/deco", `{"host":"192.0.2.5","email":"o@example.com","password":"x"}`, true); code != 409 {
		t.Fatalf("adding a second Deco: %d", code)
	}
	if code, _, _ := e.do(t, "PUT", "/api/v1/deco", `{"host":"192.0.2.1","email":"owner@example.com"}`, false); code != 403 {
		t.Fatalf("changes need the CSRF token: %d", code)
	}
	if code, _, _ := e.do(t, "PUT", "/api/v1/deco", `{"host":"192.0.2.7","email":"owner@example.com"}`, true); code != 400 {
		t.Fatalf("a new address must need the password again: %d", code)
	}
	code, m, _ = e.do(t, "PUT", "/api/v1/deco", `{"host":"192.0.2.7","email":"o@example.com","password":"decopass"}`, true)
	if code != 200 || m["host"] != "192.0.2.7" {
		t.Fatalf("%d %v", code, m)
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/nodes", "", false); code != 200 {
		t.Fatalf("nodes after a change: %d", code)
	}

	if code, _, _ := e.do(t, "DELETE", "/api/v1/deco", `{"confirm":"yes"}`, true); code != 400 {
		t.Fatalf("remove without the typed confirmation: %d", code)
	}
	if code, _, _ := e.do(t, "DELETE", "/api/v1/deco", `{"confirm":"REMOVE"}`, true); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	for _, p := range []string{"/api/v1/nodes", "/api/v1/clients", "/api/v1/network", "/api/v1/session", "/api/v1/wifi"} {
		if code, m, _ := e.do(t, "GET", p, "", false); code != 409 || m["code"] != "no_deco" {
			t.Errorf("%s with no Deco: %d %v", p, code, m)
		}
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/settings", "", false); code != 200 {
		t.Fatalf("settings work without a Deco: %d", code)
	}
	if _, m, _ := e.do(t, "GET", "/api/v1/deco", "", false); m["configured"] != false || m["running"] != false {
		t.Fatalf("%v", m)
	}
	code, m, _ = e.do(t, "POST", "/api/v1/deco", `{"host":"192.0.2.8","email":"o@example.com","password":"decopass"}`, true)
	if code != 200 || m["running"] != true {
		t.Fatalf("add again: %d %v", code, m)
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/deco", `{"host":"","email":"o@example.com","password":"x"}`, true); code != 409 {
		t.Fatalf("%d", code)
	}
}

func TestSpecIsValidYAML(t *testing.T) {
	var v map[string]any
	if err := yaml.Unmarshal(spec, &v); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshIntervals(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_, m, _ := e.do(t, "GET", "/api/v1/settings", "", false)
	if p := m["poll"].(map[string]any); p["fast"] != "1m0s" || p["source"] != "config" {
		t.Fatalf("%v", p)
	}
	for _, bad := range []string{
		`{"poll":{"fast":"5s","slow":"5m","offline_after_misses":2}}`,
		`{"poll":{"fast":"5m","slow":"1m","offline_after_misses":2}}`,
		`{"poll":{"fast":"30s","slow":"2h","offline_after_misses":2}}`,
		`{"poll":{"fast":"30s","slow":"5m","offline_after_misses":0}}`,
		`{"poll":{"fast":"soon","slow":"5m","offline_after_misses":2}}`,
		`{}`,
	} {
		if code, _, _ := e.do(t, "PUT", "/api/v1/settings", bad, true); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, m, _ := e.do(t, "PUT", "/api/v1/settings", `{"poll":{"fast":"15s","slow":"2m","offline_after_misses":3}}`, true)
	if p := m["poll"].(map[string]any); code != 200 || p["fast"] != "15s" || p["slow"] != "2m0s" || p["offline_after_misses"] != float64(3) || p["source"] != "ui" {
		t.Fatalf("%d %v", code, m)
	}
}

func TestRenameAlsoRenamesOnTheDeco(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_, m, _ := e.do(t, "GET", "/api/v1/clients/aa:bb:cc:dd:ee:01", "", false)
	if m["can_rename_on_deco"] != true {
		t.Fatalf("%v", m["can_rename_on_deco"])
	}
	code, m, _ := e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"alias":" מטבח "}`, true)
	if code != 200 || m["deco_renamed"] != true || m["alias"] != "מטבח" || len(e.fake.renames) != 1 || e.fake.renames[0] != "aa:bb:cc:dd:ee:01=מטבח" {
		t.Fatalf("%d %v %v", code, m, e.fake.renames)
	}
	// Tags alone, the Deco's own name, or local_only never touch the Deco.
	e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"tags":["kids"]}`, true)
	e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"alias":"Phone"}`, true) // what the Deco calls it
	code, m, _ = e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"alias":"Only here","local_only":true}`, true)
	if code != 200 || m["deco_renamed"] != false || len(e.fake.renames) != 1 {
		t.Fatalf("%d %v %v", code, m, e.fake.renames)
	}
	// Saving the same Atlantis-only name again, without local_only, sends it.
	code, m, _ = e.do(t, "PATCH", "/api/v1/clients/aa:bb:cc:dd:ee:01", `{"alias":"Only here"}`, true)
	if code != 200 || m["deco_renamed"] != true || len(e.fake.renames) != 2 {
		t.Fatalf("%d %v %v", code, m, e.fake.renames)
	}
	// A device the Deco doesn't list: nothing is saved.
	code, _, _ = e.do(t, "PATCH", "/api/v1/clients/02:bb:cc:dd:ee:02", `{"alias":"Gone"}`, true)
	_, m, _ = e.do(t, "GET", "/api/v1/clients/02:bb:cc:dd:ee:02", "", false)
	if code != 404 || m["client"].(map[string]any)["alias"] == "Gone" {
		t.Fatalf("%d %v", code, m["client"])
	}
}

func TestSettingsSectionsApplyAtOnce(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	// Metrics are served at the configured path; the token is write-only.
	code, m, _ := e.do(t, "PUT", "/api/v1/settings", `{"metrics":{"enabled":true,"path":"/prom","per_client":"all","token":"scrape-me"},
		"debug":{"enabled":false},"logging":{"level":"debug"},"history":{"raw_retention":"48h","rollup_retention":"720h"},
		"notifications":{"quiet_hours":"23:00-07:00","timezone":"Asia/Jerusalem"},"openwa":{"url":"http://wa.lan","default_chat":"x","api_key":"k"},
		"access":{"base_url":"https://atlantis.lan/"}}`, true)
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	if mm := m["metrics"].(map[string]any); mm["path"] != "/prom" || mm["token_set"] != true || mm["source"] != "ui" || mm["token"] != nil {
		t.Fatalf("%v", mm)
	}
	if o := m["openwa"].(map[string]any); o["api_key_set"] != true || o["api_key"] != nil {
		t.Fatalf("%v", o)
	}
	if a := m["access"].(map[string]any); a["base_url"] != "https://atlantis.lan" {
		t.Fatalf("%v", a)
	}
	get := func(path, auth string) int {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("/prom", "scrape-me"); c != 200 {
		t.Fatalf("metrics at the new path: %d", c)
	}
	if c := get("/prom", "wrong"); c != 401 {
		t.Fatalf("the token must be enforced: %d", c)
	}
	if c := get("/metrics", "scrape-me"); c == 200 {
		t.Fatal("the old metrics path must stop serving")
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/debug/raw?area=x&form=y", "", false); code != 404 {
		t.Fatalf("debug off: %d", code)
	}
	// Disabling metrics stops them; a nil token keeps it.
	e.do(t, "PUT", "/api/v1/settings", `{"metrics":{"enabled":false,"path":"/prom","per_client":"all"}}`, true)
	if c := get("/prom", "scrape-me"); c == 200 {
		t.Fatal("disabled metrics must not be served")
	}
	_, m, _ = e.do(t, "GET", "/api/v1/settings", "", false)
	if m["metrics"].(map[string]any)["token_set"] != true {
		t.Fatal("an omitted token must keep the saved one")
	}
	// All or nothing: one bad section rejects the whole request.
	code, _, _ = e.do(t, "PUT", "/api/v1/settings", `{"logging":{"level":"error"},"notifications":{"timezone":"Mars/Base"}}`, true)
	_, m, _ = e.do(t, "GET", "/api/v1/settings", "", false)
	if code != 400 || m["logging"].(map[string]any)["level"] != "debug" {
		t.Fatalf("%d %v", code, m["logging"])
	}
	// Secure cookies follow the https public URL.
	resp, _ := http.Post(e.srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"correct horse battery"}`))
	if resp != nil {
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == "atlantis_session" && !c.Secure {
				t.Fatal("an https public URL must make the cookie Secure")
			}
		}
	}
}
