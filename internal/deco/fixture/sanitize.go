package fixture

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Fixture sanitising (CLAUDE.md §6 Phase 0, §12.5): recorded device responses
// must not carry real MACs, IPs, names, identifiers or secrets. The rules err
// on the side of over-redaction. Replacements are consistent within one
// Sanitizer, so relationships (client -> node MAC) survive.

var (
	// MACs as AA-BB-.., aa:bb:.., aabb.ccdd.eeff or 12 bare hex digits.
	// Boundaries are checked by replaceBounded, not \b, so that "_" (a word
	// character) still separates an address from its neighbours.
	macRE  = regexp.MustCompile(`(?i)[0-9a-f]{2}[-:][0-9a-f]{2}(?:[-:][0-9a-f]{2}){4}|[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}|[0-9a-f]{12,}`)
	ipv4RE = regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`)
	// Email addresses (TP-Link ID usernames).
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// IPv6 candidates; each is validated with net.ParseIP before replacement.
	ipv6RE = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}`)
)

// Redact replaces the value of every secret-named key, at any depth, with
// "[redacted]". Unlike Sanitize it keeps identifiers; it is for debug output.
// Numbers are kept exactly.
func Redact(raw json.RawMessage) (json.RawMessage, error) {
	v, err := decodeExact(raw)
	if err != nil {
		return nil, err
	}
	redactValue(v)
	return json.Marshal(v)
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSecretKey(normKey(k)) {
				t[k] = "[redacted]"
			} else {
				redactValue(val)
			}
		}
	case []any:
		for _, e := range t {
			redactValue(e)
		}
	}
}

// normKey lowercases a key and drops separators: "Device_Name" -> "devicename".
func normKey(k string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(k))
}

// isSecretKey reports keys whose values are credentials of any kind.
func isSecretKey(nk string) bool {
	for _, s := range []string{"password", "passwd", "passphrase", "psk", "secret", "token", "stok", "sysauth", "cookie"} {
		if strings.Contains(nk, s) {
			return true
		}
	}
	// "pwd" and "pass" also appear as fragments (pppoe_pwd, wifi_pass); this
	// over-redacts keys like "bypass", which is the safe direction.
	return strings.Contains(nk, "pwd") || strings.Contains(nk, "pass") || strings.HasSuffix(nk, "key")
}

// isIDKey reports keys whose values identify a device, account or person.
func isIDKey(nk string) bool {
	switch nk {
	case "id", "sn", "account", "username", "owner", "ownerid", "tpid", "cloudid", "hwid", "fwid", "oemid":
		return true
	}
	// Cloud identifiers: deviceId, master_device_id, group_id, accountId,
	// terminalUUID, termID, ...
	for _, s := range []string{"serial", "uuid", "email", "deviceid", "groupid", "accountid", "userid", "termid", "terminalid"} {
		if strings.Contains(nk, s) {
			return true
		}
	}
	return false
}

// isNameKey reports keys whose values are user-chosen labels.
func isNameKey(nk string) bool {
	for _, s := range []string{"name", "nick", "ssid", "alias", "label"} {
		if strings.Contains(nk, s) {
			return true
		}
	}
	return false
}

// Sanitizer replaces identifying values in decrypted device JSON.
type Sanitizer struct {
	macs, ips, names, ids map[string]string
}

// NewSanitizer returns a Sanitizer with empty replacement tables.
func NewSanitizer() *Sanitizer {
	return &Sanitizer{macs: map[string]string{}, ips: map[string]string{}, names: map[string]string{}, ids: map[string]string{}}
}

// Sanitize returns an indented, sanitised copy of raw. Numbers are kept exactly.
func (s *Sanitizer) Sanitize(raw json.RawMessage) (json.RawMessage, error) {
	v, err := decodeExact(raw)
	if err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	return json.MarshalIndent(s.walk("", v), "", "  ")
}

// decodeExact decodes JSON keeping numbers as json.Number.
func decodeExact(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

func (s *Sanitizer) walk(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic numbering of replacements
		out := make(map[string]any, len(t))
		for _, k := range keys {
			// Keys can themselves be MACs or IPs (maps indexed by device).
			out[s.scrubText(k)] = s.walk(k, t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = s.walk(key, t[i]) // elements inherit the list's key
		}
		return t
	case json.Number:
		if nk := normKey(key); isSecretKey(nk) || isIDKey(nk) {
			return json.Number("0")
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
	nk := normKey(key)
	switch {
	case isSecretKey(nk) || isIDKey(nk):
		return "[redacted]"
	case isNameKey(nk):
		return s.name(v)
	case strings.Contains(nk, "mask") && net.ParseIP(v) != nil:
		return v // netmasks are not identifying
	case looksBase64Text(v):
		return s.name(v) // base64 text under an unlisted key is almost always a name
	}
	return s.scrubText(v)
}

// scrubText replaces every email, MAC and IP address inside s.
func (s *Sanitizer) scrubText(v string) string {
	v = emailRE.ReplaceAllString(v, "user@example.com")
	v = replaceBounded(macRE, v, isAlnum, func(m string) string {
		if len(m) > 12 && !strings.ContainsAny(m, "-:.") {
			return s.hexID(m) // a long hex run is an identifier, not a MAC
		}
		return s.mac(m)
	})
	v = replaceBounded(ipv4RE, v, func(c byte) bool { return c == '.' || (c >= '0' && c <= '9') }, func(m string) string {
		if ip := net.ParseIP(m); ip != nil {
			return s.ip(m, ip)
		}
		return m
	})
	return ipv6RE.ReplaceAllStringFunc(v, func(m string) string {
		if ip := net.ParseIP(m); ip != nil && ip.To4() == nil {
			return s.ip(m, ip)
		}
		return m
	})
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// replaceBounded replaces matches of re whose neighbouring bytes are not
// part of the token (joined(c) is false for them).
func replaceBounded(re *regexp.Regexp, v string, joined func(byte) bool, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(v, -1) {
		if (m[0] > 0 && joined(v[m[0]-1])) || (m[1] < len(v) && joined(v[m[1]])) {
			continue
		}
		b.WriteString(v[last:m[0]])
		b.WriteString(fn(v[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(v[last:])
	return b.String()
}

// hexID replaces a long hex identifier with a consistent placeholder of the
// same length.
func (s *Sanitizer) hexID(m string) string {
	k := strings.ToUpper(m)
	fake, ok := s.ids[k]
	if !ok {
		fake = fmt.Sprintf("%0*X", len(m), len(s.ids)+1)
		s.ids[k] = fake
	}
	return fake
}

// looksBase64Text reports a string that is base64 of printable UTF-8 text.
func looksBase64Text(v string) bool {
	if len(v) < 4 || len(v)%4 != 0 {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || !utf8.Valid(b) {
		return false
	}
	hasLetter := false
	for _, r := range string(b) {
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return false
		}
		hasLetter = hasLetter || unicode.IsLetter(r)
	}
	return hasLetter
}

func (s *Sanitizer) mac(m string) string {
	hexDigits := strings.NewReplacer("-", "", ":", "", ".", "").Replace(strings.ToUpper(m))
	fake, ok := s.macs[hexDigits]
	if !ok {
		n := len(s.macs) + 1
		fake = fmt.Sprintf("020000%06X", n) // locally administered, unique up to 16M
		s.macs[hexDigits] = fake
	}
	switch {
	case strings.Contains(m, "."):
		fake = fake[0:4] + "." + fake[4:8] + "." + fake[8:12]
	case strings.ContainsAny(m, "-:"):
		sep := string(m[2])
		parts := make([]string, 6)
		for i := range parts {
			parts[i] = fake[2*i : 2*i+2]
		}
		fake = strings.Join(parts, sep)
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
	n := uint32(len(s.ips) + 1)
	var fake string
	if ip.To4() != nil {
		// 198.18.0.0/15 (RFC 2544 benchmarking): 131k unique, never a real network.
		a := uint32(198)<<24 | uint32(18)<<16 + n%(1<<17-1)
		fake = fmt.Sprintf("%d.%d.%d.%d", a>>24, a>>16&0xff, a>>8&0xff, a&0xff)
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
