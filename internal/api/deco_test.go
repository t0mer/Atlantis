package api

import (
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
	if code, _, _ := e.do(t, "PUT", "/api/v1/deco", `{"host":"192.0.2.1","email":"o@example.com"}`, false); code != 403 {
		t.Fatalf("changes need the CSRF token: %d", code)
	}
	code, m, _ = e.do(t, "PUT", "/api/v1/deco", `{"host":"192.0.2.7","email":"o@example.com"}`, true)
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
