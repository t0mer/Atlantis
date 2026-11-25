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

// Seen live: a satellite has no mesh data of its own but is in its master's
// re_list; it must not become a separate site.
func TestBuildSitesFoldsSatelliteWithoutFeatures(t *testing.T) {
	devs := []cloudDevice{{DeviceID: "m1"}, {DeviceID: "s1", Alias: "TWFtYWQ="}}
	feats := map[string]feature{"m1": {Data: featureData{GroupID: "g1", Role: "master",
		ReList: []struct {
			DeviceID       string `json:"device_id"`
			MAC            string `json:"mac"`
			DeviceModel    string `json:"device_model"`
			Nickname       string `json:"nickname"`
			CustomNickname string `json:"custom_nickname"`
		}{{DeviceID: "s1"}}}}}
	sites := buildSites(devs, feats)
	if len(sites) != 1 || sites[0].ID != "g1" || len(sites[0].Nodes) != 2 {
		t.Fatalf("%+v", sites)
	}
}
