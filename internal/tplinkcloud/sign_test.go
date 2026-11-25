package tplinkcloud

import "testing"

// Vector computed independently with Python's hmac/hashlib.
func TestSignatureKnownAnswer(t *testing.T) {
	k := AppKeys{AccessKey: "ak", Secret: "test-secret"}
	body := []byte(`{"a":1}`)
	if got := contentMD5(body); got != "u2y1xo30ZSlByvZSo2by2A==" {
		t.Fatalf("contentMD5 = %s", got)
	}
	got := k.signature(body, "nonce-1", "/api/v2/account/login")
	want := "Timestamp=9999999999, Nonce=nonce-1, AccessKey=ak, Signature=1b064d2842e7e7f8ffb7f2912bb4fd46aa567bd3"
	if got != want {
		t.Fatalf("signature\n got %s\nwant %s", got, want)
	}
}

func TestPinsForWildcardMatchesOneLabel(t *testing.T) {
	cases := map[string]int{
		"n-wap-gw.i.tplinkcloud.com": 2, // *.i.tplinkcloud.com only
		"eu-wap.tplinkcloud.com":     3,
		"tplinkcloud.com":            0,
		"a.b.c.tplinkcloud.com":      0,
		"evil-tplinkcloud.com":       0,
	}
	for host, n := range cases {
		if got := len(pinsFor(DefaultPins, host)); got != n {
			t.Errorf("pinsFor(%s) = %d pins, want %d", host, got, n)
		}
	}
}

func TestTerminalUUIDIsV4(t *testing.T) {
	u := NewTerminalUUID()
	if len(u) != 36 || u[14] != '4' {
		t.Fatalf("not a v4 UUID: %s", u)
	}
}
