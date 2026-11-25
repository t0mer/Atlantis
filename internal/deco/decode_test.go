package deco

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestDecodeName(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := map[string]string{
		b64("סלון"):               "סלון",
		b64("Living room"):        "Living room",
		"Deco":                    "Deco", // valid base64, decodes to binary: keep raw
		"test":                    "test",
		"<Error Decoding iPhone>": "iPhone",
		"plain name with spaces!": "plain name with spaces!",
		"":                        "",
	}
	for in, want := range cases {
		if got := DecodeName(in); got != want {
			t.Errorf("DecodeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"AA-BB-CC-DD-EE-FF": "aa:bb:cc:dd:ee:ff",
		"aa:bb:cc:dd:ee:ff": "aa:bb:cc:dd:ee:ff",
		"AABBCCDDEEFF":      "aa:bb:cc:dd:ee:ff",
		"aabb.ccdd.eeff":    "aa:bb:cc:dd:ee:ff",
		"not-a-mac":         "not-a-mac",
		"ZZBBCCDDEEFF":      "zzbbccddeeff",
	} {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q", in, got)
		}
	}
	if WireMAC("aabbccddeeff") != "AA-BB-CC-DD-EE-FF" {
		t.Error("WireMAC")
	}
}

func TestFlexTypes(t *testing.T) {
	var v struct {
		A, B, C, D FlexInt
		E, F, G, H FlexBool
	}
	err := json.Unmarshal([]byte(`{"A":5,"B":"7","C":"","D":null,"E":true,"F":"1","G":0,"H":"off"}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.A != 5 || v.B != 7 || v.C != 0 || v.D != 0 || !v.E || !v.F || v.G || v.H {
		t.Fatalf("%+v", v)
	}
}

// Review finding: one odd value must not make the containing struct fail.
func TestFlexTypesNeverFail(t *testing.T) {
	var v struct {
		I       FlexInt
		F       FlexFloat
		B       FlexBool
		S, N, O FlexString
	}
	err := json.Unmarshal([]byte(`{"I":"n/a","F":"0.35","B":"unknown","S":"connected","N":1,"O":{"x":1}}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.I != 0 || v.F != 0.35 || v.B || v.S != "connected" || v.N != "1" || v.O != "" {
		t.Fatalf("%+v", v)
	}
}
