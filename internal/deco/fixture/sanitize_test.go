package fixture

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestSanitizeReplacesIdentifiersConsistently(t *testing.T) {
	hebrew := base64.StdEncoding.EncodeToString([]byte("הטלפון של דנה"))
	in := `{
	  "client_list": [
	    {"mac": "AA-BB-CC-11-22-33", "ip": "192.168.68.20", "name": "` + hebrew + `", "up_speed": 1200},
	    {"mac": "aa:bb:cc:11:22:44", "ip": "fe80::1c2b:3cff:fe4d:5e6f", "name": "Laptop", "online": true}
	  ],
	  "node": {"mac": "AA-BB-CC-11-22-33", "sn": "Y21C0ZZ1234", "ip_info": {"ip": "203.0.113.99", "mask": "255.255.255.0", "gateway": "0.0.0.0"}},
	  "note": "seen on AA-BB-CC-11-22-33 earlier",
	  "password": "hunter2"
	}`
	out, err := NewSanitizer().Sanitize(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, leaked := range []string{"AA-BB-CC-11-22-33", "aa:bb:cc:11:22:44", "192.168.68.20", "fe80::1c2b", "Laptop", hebrew, "Y21C0ZZ1234", "hunter2", "203.0.113.99"} {
		if strings.Contains(got, leaked) {
			t.Errorf("sanitised output still contains %q:\n%s", leaked, got)
		}
	}
	var v struct {
		ClientList []struct {
			MAC, IP, Name string
			UpSpeed       json.Number `json:"up_speed"`
		} `json:"client_list"`
		Node struct {
			MAC    string                         `json:"mac"`
			IPInfo struct{ Mask, Gateway string } `json:"ip_info"`
		} `json:"node"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.Node.MAC != v.ClientList[0].MAC {
		t.Errorf("same MAC got different replacements: %q vs %q", v.Node.MAC, v.ClientList[0].MAC)
	}
	if !strings.Contains(v.Note, v.Node.MAC) {
		t.Errorf("MAC inside free text not replaced consistently: %q", v.Note)
	}
	if strings.ToLower(v.ClientList[1].MAC) != v.ClientList[1].MAC || !strings.Contains(v.ClientList[1].MAC, ":") {
		t.Errorf("MAC format not preserved: %q", v.ClientList[1].MAC)
	}
	if dec, err := base64.StdEncoding.DecodeString(v.ClientList[0].Name); err != nil || !strings.HasPrefix(string(dec), "name-") {
		t.Errorf("base64 name should stay base64 of a placeholder: %q", v.ClientList[0].Name)
	}
	if v.Node.IPInfo.Mask != "255.255.255.0" || v.Node.IPInfo.Gateway != "0.0.0.0" {
		t.Errorf("mask/unspecified address should be kept: %+v", v.Node.IPInfo)
	}
	if v.ClientList[0].UpSpeed.String() != "1200" {
		t.Errorf("numbers must be preserved: %q", v.ClientList[0].UpSpeed)
	}
}

// Cases found by independent review: each value must not survive sanitising.
func TestSanitizeReviewLeakCases(t *testing.T) {
	in := `{
	  "a": "AABBCCDDEEFF",
	  "b": "aabb.ccdd.eeff",
	  "c": "http://192.168.68.1/x",
	  "d": "192.168.68.0/24",
	  "e": "10.0.0.5:8080",
	  "f": "8.8.8.8 1.1.1.1",
	  "nick_name": "Tomer's iPhone",
	  "deviceName": "Kids-PC",
	  "group_name": "SG9tZQ==",
	  "free_text_b64": "16nXnNeV150=",
	  "sn": 777000111222333444,
	  "owner_id": 98765,
	  "AA-BB-CC-DD-EE-01": {"10.0.0.9": true},
	  "psk_key": "secretwifi",
	  "wifi_password": "pw2",
	  "big": 12345678901234567890
	}`
	out, err := NewSanitizer().Sanitize(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, leaked := range []string{
		"AABBCCDDEEFF", "aabb.ccdd.eeff", "192.168.68.1", "192.168.68.0", "10.0.0.5", "8.8.8.8", "1.1.1.1",
		"Tomer", "Kids-PC", "SG9tZQ==", "16nXnNeV150=", "777000111222333444", "98765",
		"AA-BB-CC-DD-EE-01", "10.0.0.9", "secretwifi", "pw2",
	} {
		if strings.Contains(got, leaked) {
			t.Errorf("sanitised output still contains %q", leaked)
		}
	}
	if !strings.Contains(got, "12345678901234567890") {
		t.Errorf("large non-identifier number lost precision:\n%s", got)
	}
	if !strings.Contains(got, ":8080") || !strings.Contains(got, "/24") {
		t.Errorf("structure around embedded IPs should be kept:\n%s", got)
	}
}

// Identifiers returned by the TP-Link cloud API.
func TestSanitizeCloudIdentifiers(t *testing.T) {
	in := `{
	  "deviceId": "8012ABCDEF0123456789",
	  "master_device_id": "8012FEDCBA9876543210",
	  "group_id": "g-55aa77",
	  "accountId": "acct-4242",
	  "terminalUUID": "1b2c3d4e-0000-4000-8000-123456789abc",
	  "termID": "term-31337",
	  "token": "tok-should-vanish",
	  "cloudUserName": "someone@example.org",
	  "note": "owner is other.person@mail.example.net"
	}`
	out, err := NewSanitizer().Sanitize(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, leaked := range []string{
		"8012ABCDEF0123456789", "8012FEDCBA9876543210", "g-55aa77", "acct-4242",
		"1b2c3d4e", "term-31337", "tok-should-vanish", "someone@", "other.person@",
	} {
		if strings.Contains(got, leaked) {
			t.Errorf("sanitised output still contains %q:\n%s", leaked, got)
		}
	}
}

func TestSanitizeIPReplacementsStayUnique(t *testing.T) {
	s := NewSanitizer()
	seen := map[string]string{}
	for i := 0; i < 2000; i++ {
		real := fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff+1)
		fake := s.ip(real, net.ParseIP(real))
		if prev, dup := seen[fake]; dup {
			t.Fatalf("%s and %s both became %s", prev, real, fake)
		}
		seen[fake] = real
	}
}

func TestRawKeepsNumbersExactly(t *testing.T) {
	out, err := Redact(json.RawMessage(`{"id":12345678901234567890,"password":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "12345678901234567890") || strings.Contains(string(out), `"x"`) {
		t.Fatalf("Redact output wrong: %s", out)
	}
}
