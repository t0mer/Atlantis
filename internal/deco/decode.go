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
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
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

// FlexInt decodes a JSON number or a numeric string. It never fails:
// null, "", and values that are not numbers decode as 0, so one odd field
// cannot drop a whole node or client.
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	*f = 0
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	if b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
		b = []byte(strings.TrimSpace(s))
	}
	if v, err := strconv.ParseFloat(string(b), 64); err == nil {
		*f = FlexInt(v)
	}
	return nil
}

// FlexFloat is FlexInt for fractional values (e.g. a 0.35 CPU load).
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	*f = 0
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
		b = []byte(strings.TrimSpace(s))
	}
	if v, err := strconv.ParseFloat(string(b), 64); err == nil {
		*f = FlexFloat(v)
	}
	return nil
}

// FlexBool decodes true/false, numbers (non-zero is true) and the strings
// "true"/"1"/"on"/"yes". It never fails; anything else is false.
type FlexBool bool

func (f *FlexBool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.TrimSpace(strings.Trim(string(bytes.TrimSpace(b)), `"`)))
	switch s {
	case "true", "on", "yes":
		*f = true
	default:
		n, err := strconv.ParseFloat(s, 64)
		*f = FlexBool(err == nil && n != 0)
	}
	return nil
}

// FlexString decodes a string, or a number or boolean as its text, so a
// status that one firmware sends as "connected" and another as 1 is kept.
// It never fails; objects and arrays decode as "".
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	*f = ""
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if json.Unmarshal(b, &s) == nil {
			*f = FlexString(s)
		}
	case '{', '[':
	default:
		*f = FlexString(b)
	}
	return nil
}
