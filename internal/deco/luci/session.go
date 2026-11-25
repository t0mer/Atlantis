package luci

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
)

const formContentType = "application/x-www-form-urlencoded"

var (
	errSessionDead = errors.New("luci: session expired and re-login did not help")
	stokRE         = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	stokInURLRE    = regexp.MustCompile(`;stok=[^/?\s"]+`)
	errTimeout     = errors.New("luci: device timed out")
	sysauthRE      = regexp.MustCompile(`sysauth=[a-f0-9]+`)
)

// Options configure a Client. The zero value is usable.
type Options struct {
	// Pin is the hex SHA-256 of the device's TLS leaf certificate. When empty,
	// the first certificate seen is trusted and reported via OnFingerprint.
	Pin string
	// OnFingerprint is called with the fingerprint of a certificate seen while
	// no pin was set. The caller should persist it and pass it back as Pin.
	OnFingerprint func(fingerprint string)
	// OnSchemeChange is called with the new base URL after the device redirects
	// from http to https. The caller should persist it.
	OnSchemeChange func(baseURL string)
	// Timeout bounds each HTTP request. Defaults to 10s.
	Timeout time.Duration
	// Logger receives non-sensitive diagnostics. Defaults to slog.Default().
	Logger *slog.Logger
}

// Client is a session-managed connection to one Deco's local API.
// All device calls are serialised: the device handles concurrent requests
// on the same stok poorly.
type Client struct {
	mu    sync.Mutex
	pinMu sync.Mutex
	base  *url.URL
	pw    string
	h     string // md5("admin"+password), used in the sign string
	km    keyMaterial
	opts  Options
	pin   string
	httpc *http.Client
	log   *slog.Logger

	logged     bool
	authFailed bool
	stok       string
	cookie     string // "sysauth=<hex>"
	seq        int
	signKey    *rsa.PublicKey
}

// New creates a Client for baseURL (for example "http://192.168.68.1").
// It does not contact the device.
func New(baseURL, password string, opts Options) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("luci: invalid base URL %q", baseURL)
	}
	if password == "" {
		return nil, errors.New("luci: password is required")
	}
	km, err := newKeyMaterial()
	if err != nil {
		return nil, err
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &Client{
		base: &url.URL{Scheme: u.Scheme, Host: u.Host},
		pw:   password,
		h:    credentialHash(password),
		km:   km,
		opts: opts,
		pin:  strings.ToLower(opts.Pin),
		log:  log,
	}
	c.httpc = &http.Client{
		Timeout: opts.Timeout,
		// Handle 307 ourselves so we can switch to https deliberately.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy: nil, // LAN device: never route through an environment proxy
			TLSClientConfig: &tls.Config{
				// Verification is replaced by fingerprint pinning (see verifyPeer).
				InsecureSkipVerify:    true, //nolint:gosec // self-signed cert; pinned below
				VerifyPeerCertificate: c.verifyPeer,
			},
		},
	}
	return c, nil
}

// BaseURL returns the current base URL, which may have switched to https.
func (c *Client) BaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base.String()
}

// verifyPeer pins the leaf certificate fingerprint. Called during the TLS handshake.
func (c *Client) verifyPeer(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return errors.New("luci: device presented no certificate")
	}
	sum := sha256.Sum256(rawCerts[0])
	fp := hex.EncodeToString(sum[:])
	c.pinMu.Lock()
	first := c.pin == ""
	if first {
		c.pin = fp
	}
	match := fp == c.pin
	c.pinMu.Unlock()
	if !match {
		return deco.ErrCertChanged
	}
	if first && c.opts.OnFingerprint != nil {
		c.opts.OnFingerprint(fp)
	}
	return nil
}

// response is a fully-read HTTP response.
type response struct {
	status int
	body   []byte
	header http.Header
}

// Response size limits: API replies are small; web UI bundles can be large.
const (
	maxAPIResponseBytes  = 1 << 20
	maxPageResponseBytes = 8 << 20
)

// send POSTs body to path. See do.
func (c *Client) send(ctx context.Context, path, contentType, body, cookie string) (*response, error) {
	return c.do(ctx, http.MethodPost, path, contentType, body, cookie, maxAPIResponseBytes)
}

// do performs one request. On a 307/308 to an https URL on the same host it
// switches the base to https, persists the change, and retries once.
func (c *Client) do(ctx context.Context, method, path, contentType, body, cookie string, maxBytes int64) (*response, error) {
	for hop := 0; hop < 2; hop++ {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rd)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := c.httpc.Do(req)
		if err != nil {
			// The URL carries the session token; keep it out of error text.
			return nil, redactedError{msg: stokInURLRE.ReplaceAllString(err.Error(), ";stok=[redacted]"), err: err}
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading device response: %w", err)
		}
		if (resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusPermanentRedirect) &&
			c.followHTTPS(resp.Header.Get("Location")) == nil {
			continue
		}
		return &response{status: resp.StatusCode, body: b, header: resp.Header}, nil
	}
	return nil, errors.New("luci: too many redirects")
}

// followHTTPS switches the base to https when loc is an https URL on the same host.
func (c *Client) followHTTPS(loc string) error {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), c.base.Hostname()) {
		return errors.New("not a same-host https redirect")
	}
	c.base = &url.URL{Scheme: "https", Host: u.Host}
	c.log.Info("device redirected to https; switching scheme", "base", c.base.String())
	if c.opts.OnSchemeChange != nil {
		c.opts.OnSchemeChange(c.base.String())
	}
	return nil
}

// envelope is the JSON wrapper used by both plain and decrypted responses.
type envelope struct {
	Data         string          `json:"data"`
	ErrorCode    json.RawMessage `json:"error_code"`
	ErrorCodeAlt json.RawMessage `json:"errorcode"`
	Result       json.RawMessage `json:"result"`
}

func (e envelope) code() (int, error) {
	raw := e.ErrorCode
	if len(raw) == 0 {
		raw = e.ErrorCodeAlt
	}
	if len(raw) == 0 {
		return 0, nil
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("luci: unparseable error_code %s", raw)
	}
	if s == "timeout" {
		return 0, errTimeout
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("luci: unexpected error_code %q", s)
	}
	return n, nil
}

// plainCall sends an unencrypted JSON request (used for keys and auth).
func (c *Client) plainCall(ctx context.Context, path string, payload map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, path, "application/json", string(body), "")
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, fmt.Errorf("luci: %s returned HTTP %d", path, resp.status)
	}
	var env envelope
	if err := json.Unmarshal(resp.body, &env); err != nil {
		return nil, fmt.Errorf("luci: decoding %s: %w", path, err)
	}
	code, err := env.code()
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("luci: %s returned error_code %d", path, code)
	}
	return env.Result, nil
}

// decryptEnvelope decrypts {"data": base64(AES(json))}. dead=true means the
// device returned empty data, which indicates an expired or invalid session.
func (c *Client) decryptEnvelope(body []byte) (env envelope, dead bool, err error) {
	var outer envelope
	if err := json.Unmarshal(body, &outer); err != nil {
		return envelope{}, false, fmt.Errorf("luci: decoding response: %w", err)
	}
	if outer.Data == "" {
		code, err := outer.code()
		if err != nil {
			return envelope{}, false, err
		}
		if code != 0 {
			return envelope{}, false, fmt.Errorf("luci: device returned error_code %d", code)
		}
		return envelope{}, true, nil
	}
	enc, err := base64.StdEncoding.DecodeString(outer.Data)
	if err != nil {
		return envelope{}, false, fmt.Errorf("luci: decoding data: %w", err)
	}
	plain, err := aesDecrypt(c.km, enc)
	if err != nil {
		return envelope{}, false, fmt.Errorf("luci: decrypting response: %w", err)
	}
	if err := json.Unmarshal(plain, &env); err != nil {
		return envelope{}, false, fmt.Errorf("luci: decoding decrypted response: %w", err)
	}
	return env, false, nil
}

func sysauthHeader(h http.Header) string {
	for _, v := range h.Values("Set-Cookie") {
		if m := sysauthRE.FindString(v); m != "" {
			return m
		}
	}
	return ""
}

// loginLocked performs the four-step login. Caller holds c.mu.
func (c *Client) loginLocked(ctx context.Context) error {
	if c.authFailed {
		return deco.ErrAuthFailed
	}
	raw, err := c.plainCall(ctx, "/cgi-bin/luci/;stok=/login?form=keys", map[string]any{"operation": "read"})
	if err != nil {
		return err
	}
	var keys struct {
		Password []string `json:"password"`
	}
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys.Password) != 2 {
		return errors.New("luci: keys response missing password key")
	}
	passKey, err := parseRSAPublicKey(keys.Password[0], keys.Password[1])
	if err != nil {
		return err
	}

	raw, err = c.plainCall(ctx, "/cgi-bin/luci/;stok=/login?form=auth", map[string]any{"operation": "read"})
	if err != nil {
		return err
	}
	var auth struct {
		Key []string `json:"key"`
		Seq int      `json:"seq"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil || len(auth.Key) != 2 {
		return errors.New("luci: auth response missing sign key")
	}
	signKey, err := parseRSAPublicKey(auth.Key[0], auth.Key[1])
	if err != nil {
		return err
	}
	c.signKey, c.seq = signKey, auth.Seq

	encPass, err := rsaEncryptHex(passKey, []byte(c.pw))
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"params":    map[string]string{"password": encPass},
		"operation": "login",
	})
	if err != nil {
		return err
	}
	body, err := signedBody(c.km, signKey, c.seq, c.h, payload)
	if err != nil {
		return err
	}
	resp, err := c.send(ctx, "/cgi-bin/luci/;stok=/login?form=login", formContentType, body, "")
	if err != nil {
		return err
	}
	switch resp.status {
	case http.StatusOK:
	case http.StatusForbidden:
		return deco.ErrOtherAdmin
	default:
		return fmt.Errorf("luci: login returned HTTP %d", resp.status)
	}
	env, dead, err := c.decryptEnvelope(resp.body)
	if err != nil {
		return err
	}
	if dead {
		return errors.New("luci: login returned no data")
	}
	code, err := env.code()
	if err != nil {
		return err
	}
	switch code {
	case 0:
	case -5002:
		// Stop here: the device counts attempts and locks out.
		c.authFailed = true
		c.log.Warn("device rejected management password; polling stopped until credentials change")
		return deco.ErrWrongPassword
	default:
		return fmt.Errorf("luci: login returned error_code %d", code)
	}
	var res struct {
		Stok string `json:"stok"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || !stokRE.MatchString(res.Stok) {
		return errors.New("luci: login response missing or malformed stok")
	}
	cookie := sysauthHeader(resp.header)
	if cookie == "" {
		return errors.New("luci: login response missing sysauth cookie")
	}
	c.stok, c.cookie, c.logged = res.Stok, cookie, true
	c.log.Info("logged in to Deco", "base", c.base.String())
	return nil
}

// signedLocked performs one signed admin call. dead=true means the session is
// no longer valid. Caller holds c.mu and must be logged in.
func (c *Client) signedLocked(ctx context.Context, area, form, op string, params any) (res json.RawMessage, dead bool, err error) {
	payload := map[string]any{"operation": op}
	if params != nil {
		payload["params"] = params
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	body, err := signedBody(c.km, c.signKey, c.seq, c.h, pb)
	if err != nil {
		return nil, false, err
	}
	path := fmt.Sprintf("/cgi-bin/luci/;stok=%s/admin/%s?form=%s", c.stok, area, form)
	resp, err := c.send(ctx, path, formContentType, body, c.cookie)
	if err != nil {
		return nil, false, err
	}
	if resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden {
		return nil, true, nil
	}
	if resp.status != http.StatusOK {
		return nil, false, fmt.Errorf("luci: %s/%s returned HTTP %d", area, form, resp.status)
	}
	env, dead, err := c.decryptEnvelope(resp.body)
	if err != nil || dead {
		return nil, dead, err
	}
	code, err := env.code()
	if err != nil {
		return nil, false, err
	}
	if code != 0 {
		return nil, false, fmt.Errorf("luci: %s/%s returned error_code %d", area, form, code)
	}
	return env.Result, false, nil
}

func (c *Client) clearLocked() {
	c.logged = false
	c.stok = ""
	c.cookie = ""
}

// call runs a signed admin call, logging in lazily. If the session is dead it
// logs in once more and retries once.
func (c *Client) call(ctx context.Context, area, form, op string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authFailed {
		return nil, deco.ErrAuthFailed
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !c.logged {
			if err := c.loginLocked(ctx); err != nil {
				return nil, err
			}
		}
		res, dead, err := c.signedLocked(ctx, area, form, op, params)
		if err != nil {
			return nil, err
		}
		if !dead {
			return res, nil
		}
		c.log.Info("session invalid; re-logging in", "area", area, "form", form)
		c.clearLocked()
	}
	return nil, errSessionDead
}

// SetPassword replaces the management password and clears a previous
// ErrWrongPassword, so polling can resume. Call it after the user fixes credentials.
func (c *Client) SetPassword(password string) error {
	if password == "" {
		return errors.New("luci: password is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pw = password
	c.h = credentialHash(password)
	c.authFailed = false
	c.clearLocked()
	return nil
}

// redactedError is an error whose text has been scrubbed but which still unwraps to the original.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

// Login establishes a session. It is a no-op when already logged in.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logged {
		return nil
	}
	return c.loginLocked(ctx)
}

// Logout releases the device admin session. Local state is cleared even if the call fails.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.logged {
		return nil
	}
	_, _, err := c.signedLocked(ctx, "system", "logout", "logout", nil)
	c.clearLocked()
	return err
}
