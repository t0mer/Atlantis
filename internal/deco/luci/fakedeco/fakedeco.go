// Package fakedeco is an httptest-backed stand-in for a TP-Link Deco local API.
// It implements the same crypto as the client, independently, so tests exercise
// the real wire format. It is test-only and must not be imported by production code.
package fakedeco

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" //nolint:gosec // protocol-mandated
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const adminPrefix = "/cgi-bin/luci/;stok="

// Fake is a running fake Deco. Plain serves http and redirects to TLS when
// RedirectToTLS is set; TLS serves the same API over https with a self-signed cert.
type Fake struct {
	Password string
	Plain    *httptest.Server
	TLS      *httptest.Server

	mu            sync.Mutex
	passPriv      *rsa.PrivateKey
	signPriv      *rsa.PrivateKey
	seq           int
	stok          string
	sysauth       string
	logins        int
	expireIn      int // authed calls before the session is invalidated; 0 = never
	wrongPassword bool
	busy          bool
	redirectToTLS bool
	results       map[string]any
}

type key struct{ k, i string }

// New starts a fake Deco that accepts password. Read results are preset with defaults.
func New(password string) (*Fake, error) {
	pp, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	sp, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	f := &Fake{
		Password: password,
		passPriv: pp,
		signPriv: sp,
		seq:      7001,
		results:  defaultResults(),
	}
	f.Plain = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		redirect := f.redirectToTLS
		f.mu.Unlock()
		if redirect {
			w.Header().Set("Location", f.TLS.URL+r.URL.RequestURI())
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		f.serve(w, r)
	}))
	f.TLS = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	return f, nil
}

// Close shuts down both servers.
func (f *Fake) Close() {
	f.Plain.Close()
	f.TLS.Close()
}

// Update runs fn while holding the fake's lock. Use it to change behaviour between requests.
func (f *Fake) Update(fn func(f *Fake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// Logins returns how many successful logins the fake has served.
func (f *Fake) Logins() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

// Behaviour and data setters (call via Update).
func (f *Fake) SetWrongPassword(v bool)     { f.wrongPassword = v }
func (f *Fake) SetBusy(v bool)              { f.busy = v }
func (f *Fake) SetRedirectToTLS(v bool)     { f.redirectToTLS = v }
func (f *Fake) SetExpireAfter(n int)        { f.expireIn = n }
func (f *Fake) SetResult(key string, v any) { f.results[key] = v }

// TLSFingerprint returns the hex SHA-256 of the TLS leaf certificate.
func (f *Fake) TLSFingerprint() string {
	sum := sha256.Sum256(f.TLS.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

func defaultResults() map[string]any {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	return map[string]any{
		"device/device_list": map[string]any{
			"device_list": []any{
				map[string]any{
					"mac": "AA-BB-CC-DD-EE-01", "nickname": "", "custom_nickname": b64("שלום"),
					"role": "master", "device_model": "Deco X20", "device_ip": "192.168.68.1",
					"hardware_ver": "1.0", "software_ver": "1.2.3", "group_status": 1, "inet_status": 1,
					"signal_strength": map[string]int{"band2_4": 0, "band5": 0},
					"rx_rate_list":    []int{0}, "tx_rate_list": []int{0}, "previous": "", "port_count": 4,
				},
			},
		},
	}
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rest, ok := strings.CutPrefix(r.URL.Path, adminPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	form := r.URL.Query().Get("form")
	if strings.HasPrefix(rest, "/login") {
		f.serveLogin(w, r, form)
		return
	}
	stokPart, area, found := strings.Cut(rest, "/admin/")
	if !found {
		http.NotFound(w, r)
		return
	}
	f.serveAdmin(w, r, stokPart, area, form)
}

func (f *Fake) serveLogin(w http.ResponseWriter, r *http.Request, form string) {
	switch form {
	case "keys":
		writeJSON(w, map[string]any{"error_code": 0, "result": map[string]any{"password": keyPair(f.passPriv)}})
	case "auth":
		writeJSON(w, map[string]any{"error_code": 0, "result": map[string]any{"key": keyPair(f.signPriv), "seq": f.seq}})
	case "login":
		f.handleLogin(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *Fake) handleLogin(w http.ResponseWriter, r *http.Request) {
	if f.busy {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	k, _, payload, err := f.openSigned(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var p struct {
		Params    map[string]string `json:"params"`
		Operation string            `json:"operation"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.Operation != "login" {
		http.Error(w, "bad login payload", http.StatusBadRequest)
		return
	}
	pass, err := rsaDecryptHex(f.passPriv, p.Params["password"])
	if err != nil || f.wrongPassword || string(pass) != f.Password {
		writeSigned(w, k, map[string]any{"error_code": -5002, "result": map[string]any{"attemptsAllowed": 5}})
		return
	}
	f.logins++
	f.stok = randHex()
	f.sysauth = randHex()
	w.Header().Set("Set-Cookie", "sysauth="+f.sysauth+"; Path=/")
	writeSigned(w, k, map[string]any{"error_code": 0, "result": map[string]any{"stok": f.stok}})
}

func (f *Fake) serveAdmin(w http.ResponseWriter, r *http.Request, stokPart, area, form string) {
	cookie, _ := r.Cookie("sysauth")
	if f.stok == "" || stokPart != f.stok || cookie == nil || cookie.Value != f.sysauth {
		// Empty data signals an invalid session to the client.
		writeJSON(w, map[string]any{"data": ""})
		return
	}
	k, h, _, err := f.openSigned(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if h != credHash(f.Password) {
		http.Error(w, "bad credential hash", http.StatusBadRequest)
		return
	}
	if form == "logout" {
		f.stok, f.sysauth = "", ""
		writeSigned(w, k, map[string]any{"error_code": 0, "result": map[string]any{}})
	} else if res, ok := f.results[area+"/"+form]; ok {
		writeSigned(w, k, map[string]any{"error_code": 0, "result": res})
	} else {
		writeSigned(w, k, map[string]any{"error_code": -1, "result": map[string]any{}})
	}
	// The session dies after this response once the countdown reaches zero.
	if f.expireIn > 0 {
		f.expireIn--
		if f.expireIn == 0 {
			f.stok = ""
		}
	}
}

// openSigned verifies and decrypts a signed request body.
// It checks that s == seq + len(data), which catches client sequence bugs.
func (f *Fake) openSigned(r *http.Request) (key, string, []byte, error) {
	if err := r.ParseForm(); err != nil {
		return key{}, "", nil, err
	}
	signPlain, err := rsaDecryptHex(f.signPriv, r.PostForm.Get("sign"))
	if err != nil {
		return key{}, "", nil, fmt.Errorf("sign: %w", err)
	}
	vals, err := url.ParseQuery(string(signPlain))
	if err != nil {
		return key{}, "", nil, err
	}
	data := r.PostForm.Get("data")
	s, err := strconv.Atoi(vals.Get("s"))
	if err != nil || s != f.seq+len(data) {
		return key{}, "", nil, fmt.Errorf("bad s: got %q want %d", vals.Get("s"), f.seq+len(data))
	}
	k := key{vals.Get("k"), vals.Get("i")}
	enc, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return key{}, "", nil, err
	}
	payload, err := aesDecrypt(k, enc)
	if err != nil {
		return key{}, "", nil, err
	}
	return k, vals.Get("h"), payload, nil
}

func keyPair(p *rsa.PrivateKey) []string {
	return []string{p.N.Text(16), fmt.Sprintf("%06x", p.E)}
}

func rsaDecryptHex(priv *rsa.PrivateKey, s string) ([]byte, error) {
	blk := 2 * priv.Size()
	if len(s) == 0 || len(s)%blk != 0 {
		return nil, errors.New("bad RSA ciphertext length")
	}
	var out []byte
	for i := 0; i < len(s); i += blk {
		raw, err := hex.DecodeString(s[i : i+blk])
		if err != nil {
			return nil, err
		}
		pt, err := rsa.DecryptPKCS1v15(nil, priv, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, pt...)
	}
	return out, nil
}

func aesEncrypt(k key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher([]byte(k.k))
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(k.i)).CryptBlocks(out, padded)
	return out, nil
}

func aesDecrypt(k key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher([]byte(k.k))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("bad AES length")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, []byte(k.i)).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, errors.New("bad padding")
	}
	return out[:len(out)-pad], nil
}

func writeSigned(w http.ResponseWriter, k key, inner map[string]any) {
	b, _ := json.Marshal(inner)
	enc, err := aesEncrypt(k, b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"data": base64.StdEncoding.EncodeToString(enc)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func credHash(pw string) string {
	sum := md5.Sum([]byte("admin" + pw)) //nolint:gosec // protocol-mandated
	return hex.EncodeToString(sum[:])
}
