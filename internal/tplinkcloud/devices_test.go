package tplinkcloud

import "testing"

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"AABBCCDDEEFF":      "aa:bb:cc:dd:ee:ff",
		"AA-BB-CC-DD-EE-FF": "aa:bb:cc:dd:ee:ff",
		"bogus":             "bogus",
	} {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q", in, got)
		}
	}
}
