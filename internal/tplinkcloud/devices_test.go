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

// Seen live: a unit without mesh feature data is named by its base64 alias.
func TestBuildSitesDecodesBase64Alias(t *testing.T) {
	sites := buildSites([]cloudDevice{{DeviceID: "d1", Alias: "TWFtYWQ="}}, nil)
	if len(sites) != 1 || sites[0].Name != "Mamad" || sites[0].ID != "d1" {
		t.Fatalf("%+v", sites)
	}
}
