// Package fakecloud is an in-process TP-Link cloud for tests. It checks every
// request's signature with its own implementation of the signing scheme, and
// serves login (with optional two-step verification), the device list and
// feature info, and a passthrough relay handled by a caller-supplied func.
package fakecloud

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
)

// Test credentials. They are not the app's real keys.
const (
	AccessKey = "fake-access-key"
	Secret    = "fake-secret"
	Email     = "owner@example.com"
	Password  = "correct horse"
	MFACode   = "123456"
)

// Device is a cloud-bound Deco unit.
type Device struct {
	ID, MAC, Model, Name string
	Online               bool
	Master               bool
	GroupID, GroupName   string // GroupName is sent base64-encoded
	Satellites           []Satellite
}

// Satellite is an entry in a master's re_list.
type Satellite struct{ ID, MAC, Model, Nickname string }

// Fake is the server. Configure it before use; fields are guarded by mu.
type Fake struct {
	*httptest.Server

	mu          sync.Mutex
	requireMFA  bool
	mfaPassed   bool
	tokens      map[string]bool
	nextToken   int
	devices     []Device
	logins      int
	codesSent   int
	paths       []string
	passthrough func(deviceID string, requestData json.RawMessage) (any, error)
}

// New starts a fake cloud.
func New() *Fake {
	f := &Fake{tokens: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// SetRequireMFA makes login answer with a two-step verification challenge.
func (f *Fake) SetRequireMFA(v bool) { f.mu.Lock(); f.requireMFA = v; f.mu.Unlock() }

// SetDevices replaces the account's devices.
func (f *Fake) SetDevices(d []Device) { f.mu.Lock(); f.devices = d; f.mu.Unlock() }

// SetPassthrough installs the relay handler. Its result becomes responseData.
func (f *Fake) SetPassthrough(h func(deviceID string, requestData json.RawMessage) (any, error)) {
	f.mu.Lock()
	f.passthrough = h
	f.mu.Unlock()
}

// ExpireTokens invalidates every issued token.
func (f *Fake) ExpireTokens() { f.mu.Lock(); f.tokens = map[string]bool{}; f.mu.Unlock() }

// Logins counts successful logins.
func (f *Fake) Logins() int { f.mu.Lock(); defer f.mu.Unlock(); return f.logins }

// CodesSent counts verification-code requests.
func (f *Fake) CodesSent() int { f.mu.Lock(); defer f.mu.Unlock(); return f.codesSent }

// Paths lists the request paths seen, in order.
func (f *Fake) Paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

var authRE = regexp.MustCompile(`^Timestamp=(\d+), Nonce=([^,]+), AccessKey=([^,]+), Signature=([0-9a-f]{40})$`)

// verify re-derives the signature independently of the client code.
func verify(r *http.Request, body []byte) error {
	m := authRE.FindStringSubmatch(r.Header.Get("X-Authorization"))
	if m == nil {
		return fmt.Errorf("malformed X-Authorization %q", r.Header.Get("X-Authorization"))
	}
	if m[1] != "9999999999" || m[3] != AccessKey {
		return fmt.Errorf("bad timestamp or access key")
	}
	sum := md5.Sum(body)
	md := base64.StdEncoding.EncodeToString(sum[:])
	if r.Header.Get("Content-MD5") != md {
		return fmt.Errorf("Content-MD5 mismatch")
	}
	mac := hmac.New(sha1.New, []byte(Secret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", md, m[1], m[2], r.URL.EscapedPath())
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(m[4])) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

func reply(w http.ResponseWriter, code int, msg string, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"error_code": code, "msg": msg, "result": result})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := verify(r, body); err != nil {
		reply(w, -20101, err.Error(), nil) // the real code is unknown; any non-zero suffices
		return
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		reply(w, -10100, "json format error", nil)
		return
	}
	str := func(k string) string { var s string; json.Unmarshal(req[k], &s); return s }

	switch r.URL.Path {
	case "/api/v2/account/getAccountStatusAndUrl":
		reply(w, 0, "", map[string]any{"appServerUrlV2": f.URL, "status": 1})
	case "/api/v2/account/login":
		if str("cloudUserName") != Email || str("cloudPassword") != Password {
			reply(w, -20601, "account or password incorrect", map[string]any{"remainAttempts": 4})
			return
		}
		if f.requireMFA && !f.mfaPassed {
			reply(w, -20677, "mfa required", map[string]any{
				"MFAProcessId": "proc-1", "supportedMFATypes": []int{2, 3}, "mfaEmail": "o***@example.com",
			})
			return
		}
		f.issue(w)
	case "/api/v2/account/getEmailVC4TerminalMFA":
		f.codesSent++
		reply(w, 0, "", nil)
	case "/api/v2/account/checkMFACodeAndLogin":
		if str("MFAProcessId") != "proc-1" || str("code") != MFACode {
			reply(w, -20676, "verification code incorrect", nil)
			return
		}
		f.mfaPassed = true
		f.issue(w)
	default:
		if !f.tokens[r.URL.Query().Get("token")] {
			reply(w, -20651, "token expired", nil)
			return
		}
		f.serveAuthed(w, r.URL.Path, req)
	}
}

func (f *Fake) issue(w http.ResponseWriter) {
	f.nextToken++
	tok := fmt.Sprintf("tok-%d", f.nextToken)
	f.tokens[tok] = true
	f.logins++
	reply(w, 0, "", map[string]any{"token": tok, "accountId": "acct-1", "email": Email})
}

func (f *Fake) serveAuthed(w http.ResponseWriter, path string, req map[string]json.RawMessage) {
	switch path {
	case "/api/v2/common/getDeviceListByPage":
		var index, limit int
		json.Unmarshal(req["index"], &index)
		json.Unmarshal(req["limit"], &limit)
		var list []map[string]any
		for i := index; i < len(f.devices) && len(list) < limit; i++ {
			d := f.devices[i]
			status := 0
			if d.Online {
				status = 1
			}
			list = append(list, map[string]any{
				"deviceId": d.ID, "deviceType": "HOMEWIFISYSTEM", "deviceMac": d.MAC,
				"deviceModel": d.Model, "alias": d.Name, "appServerUrlV2": f.URL,
				"role": 0, "status": status,
			})
		}
		reply(w, 0, "", map[string]any{"currentIndex": index, "totalNum": len(f.devices), "deviceList": list})
	case "/api/v2/common/getDeviceFeatureInfo":
		var ids []string
		json.Unmarshal(req["deviceIdList"], &ids)
		info := map[string]any{}
		for _, id := range ids {
			for _, d := range f.devices {
				if d.ID != id {
					continue
				}
				role := "slave"
				if d.Master {
					role = "master"
				}
				var re []map[string]any
				for _, s := range d.Satellites {
					re = append(re, map[string]any{
						"device_id": s.ID, "mac": s.MAC, "device_model": s.Model,
						"nickname": base64.StdEncoding.EncodeToString([]byte(s.Nickname)),
					})
				}
				info[id] = map[string]any{"version": 1, "data": map[string]any{
					"group_id": d.GroupID, "role": role, "re_list": re,
					"group_name": base64.StdEncoding.EncodeToString([]byte(d.GroupName)),
				}, "tmp_data": map[string]any{"operation_mode": "Router"}}
			}
		}
		reply(w, 0, "", map[string]any{"featureInfo": info})
	case "/api/v2/common/passthrough":
		var id string
		json.Unmarshal(req["deviceId"], &id)
		if f.passthrough == nil {
			reply(w, -20571, "device offline", nil)
			return
		}
		h := f.passthrough
		f.mu.Unlock() // the handler may be slow; don't hold the lock
		resp, err := h(id, req["requestData"])
		f.mu.Lock()
		if err != nil {
			reply(w, -20002, err.Error(), nil)
			return
		}
		reply(w, 0, "", map[string]any{"responseData": resp})
	default:
		reply(w, -40210, "method not found", nil)
	}
}
