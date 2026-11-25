package luci

import (
	"encoding/base64"
	"encoding/json"
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
