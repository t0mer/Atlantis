package luci

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Fixture sanitising (CLAUDE.md §6 Phase 0, §12.5): recorded device responses
// must not carry real MACs, IPs, names or identifiers. Replacements are
// consistent within one Sanitizer, so relationships (client -> node MAC) survive.

var (
	macRE = regexp.MustCompile(`\b[0-9A-Fa-f]{2}([-:])[0-9A-Fa-f]{2}(?:[-:][0-9A-Fa-f]{2}){4}\b`)

	nameKeys = set("name", "nickname", "custom_nickname", "ssid", "hostname", "host_name",
		"client_name", "owner_name", "device_name", "alias")
	idKeys = set("sn", "serial", "serial_number", "device_id", "hw_id", "fw_id", "oem_id",
		"uuid", "owner_id", "account", "email", "tp_id", "username")
)

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// Sanitizer replaces identifying values in decrypted device JSON.
type Sanitizer struct {
	macs, ips, names map[string]string
}

// NewSanitizer returns a Sanitizer with empty replacement tables.
func NewSanitizer() *Sanitizer {
	return &Sanitizer{macs: map[string]string{}, ips: map[string]string{}, names: map[string]string{}}
}

// Sanitize returns an indented, sanitised copy of raw.
func (s *Sanitizer) Sanitize(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers exactly as the device sent them
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	return json.MarshalIndent(s.walk("", v), "", "  ")
}

func (s *Sanitizer) walk(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic numbering of replacements
		for _, k := range keys {
			t[k] = s.walk(k, t[k])
		}
		return t
	case []any:
		for i := range t {
			t[i] = s.walk(key, t[i]) // elements inherit the list's key
		}
		return t
	case string:
		return s.str(key, t)
	}
	return v
}

func (s *Sanitizer) str(key, v string) string {
	if v == "" {
		return v
	}
	lk := strings.ToLower(key)
	switch {
	case secretKeys[lk] || idKeys[lk]:
		return "[redacted]"
	case nameKeys[lk]:
		return s.name(v)
	}
	if ip := net.ParseIP(v); ip != nil && !strings.Contains(lk, "mask") {
		return s.ip(v, ip)
	}
	return macRE.ReplaceAllStringFunc(v, s.mac)
}

func (s *Sanitizer) mac(m string) string {
	key := strings.ToUpper(strings.ReplaceAll(m, ":", "-"))
	fake, ok := s.macs[key]
	if !ok {
		n := len(s.macs) + 1
		fake = fmt.Sprintf("02-00-00-00-%02X-%02X", n>>8&0xff, n&0xff) // locally administered
		s.macs[key] = fake
	}
	if strings.Contains(m, ":") {
		fake = strings.ReplaceAll(fake, "-", ":")
	}
	if m == strings.ToLower(m) {
		fake = strings.ToLower(fake)
	}
	return fake
}

func (s *Sanitizer) ip(v string, ip net.IP) string {
	if ip.IsUnspecified() || ip.IsLoopback() {
		return v
	}
	if fake, ok := s.ips[v]; ok {
		return fake
	}
	n := len(s.ips) + 1
	var fake string
	if ip.To4() != nil {
		// Documentation ranges (RFC 5737), 254 addresses each.
		nets := []string{"192.0.2", "198.51.100", "203.0.113"}
		i := min((n-1)/254, len(nets)-1)
		fake = fmt.Sprintf("%s.%d", nets[i], min((n-1)%254+1, 254))
	} else {
		fake = fmt.Sprintf("2001:db8::%x", n) // RFC 3849
	}
	s.ips[v] = fake
	return fake
}

func (s *Sanitizer) name(v string) string {
	if fake, ok := s.names[v]; ok {
		return fake
	}
	plain := fmt.Sprintf("name-%d", len(s.names)+1)
	fake := plain
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) > 0 && utf8.Valid(b) {
		fake = base64.StdEncoding.EncodeToString([]byte(plain)) // keep the wire encoding
	}
	s.names[v] = fake
	return fake
}
