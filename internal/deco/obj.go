package deco

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Obj is a JSON object read leniently: every getter returns a zero value
// instead of failing when a field is missing or has an unexpected type.
// Transports use it so that one odd field on some firmware never drops a
// whole node, client or band.
type Obj map[string]json.RawMessage

// ParseObj decodes raw as an object; anything else gives an empty Obj and
// false.
func ParseObj(raw json.RawMessage) (Obj, bool) {
	var o Obj
	if json.Unmarshal(raw, &o) != nil || o == nil {
		return Obj{}, false
	}
	return o, true
}

// Has reports whether the field is present and not null.
func (o Obj) Has(k string) bool {
	v, ok := o[k]
	return ok && string(bytes.TrimSpace(v)) != "null"
}

// Str returns a string field; numbers and booleans as their text.
func (o Obj) Str(k string) string {
	var s FlexString
	_ = s.UnmarshalJSON(o[k])
	return string(s)
}

// Int returns a numeric field (number or numeric string), else 0.
func (o Obj) Int(k string) int64 {
	var n FlexInt
	_ = n.UnmarshalJSON(o[k])
	return int64(n)
}

// Float returns a numeric field as float64, else 0.
func (o Obj) Float(k string) float64 {
	var f FlexFloat
	_ = f.UnmarshalJSON(o[k])
	return float64(f)
}

// Bool returns a boolean field (true/false, 0/1, "true"/"1"/"on"), else false.
func (o Obj) Bool(k string) bool {
	var b FlexBool
	_ = b.UnmarshalJSON(o[k])
	return bool(b)
}

// Strs returns a list of strings; a single string becomes a one-element
// list, and non-string elements are kept as their text.
func (o Obj) Strs(k string) []string {
	raw := bytes.TrimSpace(o[k])
	if len(raw) == 0 {
		return nil
	}
	if raw[0] != '[' {
		if s := o.Str(k); s != "" {
			return []string{s}
		}
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		var s FlexString
		_ = s.UnmarshalJSON(it)
		if s != "" {
			out = append(out, string(s))
		}
	}
	return out
}

// Obj returns a nested object field, or an empty Obj.
func (o Obj) Obj(k string) Obj {
	sub, _ := ParseObj(o[k])
	return sub
}

// Objs returns the object elements of a list field; non-object elements
// are skipped.
func (o Obj) Objs(k string) []Obj {
	var items []json.RawMessage
	if json.Unmarshal(o[k], &items) != nil {
		return nil
	}
	out := make([]Obj, 0, len(items))
	for _, it := range items {
		if sub, ok := ParseObj(it); ok {
			out = append(out, sub)
		}
	}
	return out
}

// Ints returns an object of numbers (e.g. signal levels per band).
func (o Obj) Ints(k string) map[string]int {
	sub := o.Obj(k)
	if len(sub) == 0 {
		return nil
	}
	out := make(map[string]int, len(sub))
	for key := range sub {
		out[key] = int(sub.Int(key))
	}
	return out
}

// Bands is every Wi-Fi band key the Deco app knows (WirelessInfoBean).
// Matching against this list, not a "band" prefix, keeps unrelated keys
// such as a future "band_steering" out of band handling.
var Bands = []string{"band2_4", "band5_1", "band5_2", "band6", "band6_2", "band60"}

// IsBand reports whether k is a known Wi-Fi band key.
func IsBand(k string) bool {
	for _, b := range Bands {
		if k == b {
			return true
		}
	}
	return false
}

// TitleFirst uppercases the first rune of s (rune-safe, unlike s[:1]).
func TitleFirst(s string) string {
	for i, r := range s {
		return strings.ToUpper(string(r)) + s[i+len(string(r)):]
	}
	return s
}
