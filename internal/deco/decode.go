package deco

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Helpers shared by the transports for the quirks every Deco reply has:
// base64 names, mixed MAC formats, and numbers or booleans that arrive as
// strings on some firmware.

var errorDecodingRE = regexp.MustCompile(`^<Error Decoding (.*)>$`)

// DecodeName turns a device-provided name (base64 UTF-8 on the wire) into
// text. Values that are not strict base64 of valid UTF-8 are returned as
// given, with the legacy "<Error Decoding X>" wrapper unwrapped to X.
func DecodeName(s string) string {
	if s == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) > 0 && utf8.Valid(b) && printable(b) {
		return string(b)
	}
	if m := errorDecodingRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// printable rejects decodings with control bytes: a raw name that happens
// to be valid base64 ("Deco", "test") decodes to binary, not text.
func printable(b []byte) bool {
	for _, r := range string(b) {
		if r < 0x20 && r != '\t' || r == 0x7f || r == utf8.RuneError {
			return false
		}
	}
	return true
}

// NormalizeMAC returns aa:bb:cc:dd:ee:ff for AA-BB-CC-DD-EE-FF,
// aa:bb:cc:dd:ee:ff, aabb.ccdd.eeff or AABBCCDDEEFF. Anything else is
// returned lowercased.
func NormalizeMAC(s string) string {
	h := strings.ToLower(strings.NewReplacer("-", "", ":", "", ".", "").Replace(strings.TrimSpace(s)))
	if len(h) != 12 {
		return strings.ToLower(s)
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return strings.ToLower(s)
		}
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// WireMAC converts aa:bb:cc:dd:ee:ff to the device's AA-BB-CC-DD-EE-FF.
func WireMAC(s string) string { return strings.ToUpper(strings.ReplaceAll(NormalizeMAC(s), ":", "-")) }

// FlexInt decodes a JSON number, a numeric string, or null/"" (as 0).
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = FlexInt(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = FlexInt(v)
	return nil
}

// FlexBool decodes true/false, 0/1, or "true"/"false"/"0"/"1"/"on"/"off".
type FlexBool bool

func (f *FlexBool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.Trim(string(bytes.TrimSpace(b)), `"`))
	switch s {
	case "true", "1", "on", "yes":
		*f = true
	case "false", "0", "off", "no", "", "null":
		*f = false
	default:
		var n float64
		if err := json.Unmarshal([]byte(s), &n); err != nil {
			return err
		}
		*f = n != 0
	}
	return nil
}
