// Package tplinkcloud is a client for the TP-Link ID cloud as used by the Deco
// app: account login (with two-step verification), the list of Deco sites on
// the account, and the signed passthrough relay to a site's main Deco.
//
// The protocol was taken from the Deco Android app (docs/tmp-protocol.md).
// Secrets (password, account token) never appear in errors or logs.
package tplinkcloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBootstrapURL is the host the app asks for an account's region.
	DefaultBootstrapURL = "https://n-wap-gw.i.tplinkcloud.com"
	// AppType identifies the Deco Android app to the cloud.
	AppType = "TP-Link_aria_Android"
	// DefaultAppVersion is the app version the protocol was taken from.
	DefaultAppVersion = "3.10.669"

	maxResponseBytes = 4 << 20
)

// Error codes the cloud returns (docs/tmp-protocol.md §4-5).
const (
	CodeTokenExpired = -20651
	CodeMFARequired  = -20677
)

// authCodes are the codes the app treats as authentication failures
// (CloudException.isCloudAuthException). Atlantis never retries them.
var authCodes = map[int]bool{
	-23029: true, -23003: true, -20684: true, -20681: true, -20661: true,
	-20615: true, -20607: true, -20004: true, -10306: true, -20676: true,
	-20675: true, -20602: true, -20601: true, -20600: true, -20202: true,
	-20201: true, -20200: true,
}

var (
	// ErrAuthFailed means the cloud rejected the credentials. Do not retry.
	ErrAuthFailed = errors.New("tplinkcloud: authentication failed")
	// ErrNotLoggedIn is returned by calls made before a successful login.
	ErrNotLoggedIn = errors.New("tplinkcloud: not logged in")
)

// APIError is a non-zero error_code from the cloud.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return fmt.Sprintf("tplinkcloud: error %d: %s", e.Code, e.Msg) }

// Is lets errors.Is(err, ErrAuthFailed) match authentication codes.
func (e *APIError) Is(target error) bool { return target == ErrAuthFailed && authCodes[e.Code] }

// DefaultPins are the SPKI SHA-256 pins the app sets for the cloud hosts
// (com/tplink/cloud/https/u.java). Each pattern matches exactly one label.
var DefaultPins = map[string][]string{
	"*.tplinkcloud.com": {
		"hAHh3ODubSpeYae2zl9Pr1Q7dfzqgrk9H++q9r3Qg20=",
		"FWN0bUK+G5UtW1TnZEs2o7RvMbuwcVO5Dv69SGU/xwQ=",
		"esp3Rf3jZTN/NshwLAi/Yv76ZLtGJYHByPCPpCDc2Kg=",
	},
	"*.i.tplinkcloud.com": {
		"sWOm6BARva1IdHBJi44hKEhzNuy0CHXHZMe/wdoj2UM=",
		"Fu9isq+p0sM2EVoXFnUb+tJIKh9DE7LumIEH+g5I1QI=",
	},
}

// Options configure a Client.
type Options struct {
	Keys         AppKeys
	TerminalUUID string // stable per installation; see NewTerminalUUID
	BootstrapURL string // default DefaultBootstrapURL
	AppVersion   string // default DefaultAppVersion
	Locale       string // default en_US
	RootCAs      *x509.CertPool
	// Pins maps host patterns to base64 SPKI SHA-256 pins. nil means
	// DefaultPins; an empty map disables pinning (tests only).
	Pins    map[string][]string
	Timeout time.Duration // default 30s, as in the app
	// AllowHTTP permits plain-http server URLs. Tests only.
	AllowHTTP bool
}

// Client talks to the TP-Link cloud. It is safe for concurrent use.
type Client struct {
	opts  Options
	httpc *http.Client

	mu         sync.Mutex
	accountURL string
	token      string
	accountID  string
	email      string
	password   string
	mfa        *MFAChallenge
}

// New validates opts and returns a Client.
func New(opts Options) (*Client, error) {
	if !opts.Keys.valid() {
		return nil, errors.New("tplinkcloud: app access key and secret are required")
	}
	if opts.TerminalUUID == "" {
		return nil, errors.New("tplinkcloud: terminal UUID is required")
	}
	if opts.BootstrapURL == "" {
		opts.BootstrapURL = DefaultBootstrapURL
	}
	if opts.AppVersion == "" {
		opts.AppVersion = DefaultAppVersion
	}
	if opts.Locale == "" {
		opts.Locale = "en_US"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Pins == nil {
		opts.Pins = DefaultPins
	}
	tlsConf := &tls.Config{
		MinVersion:       tls.VersionTLS12,
		RootCAs:          opts.RootCAs,
		VerifyConnection: pinVerifier(opts.Pins),
	}
	return &Client{
		opts: opts,
		httpc: &http.Client{
			Timeout:   opts.Timeout,
			Transport: &http.Transport{TLSClientConfig: tlsConf, Proxy: http.ProxyFromEnvironment},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// NewTerminalUUID returns a random UUIDv4 to persist as the installation's
// terminal ID. The cloud uses it to recognise trusted (MFA-bound) terminals.
func NewTerminalUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// pinVerifier enforces SPKI pins for hosts that match a pattern. Hosts that
// match no pattern only get standard certificate verification.
func pinVerifier(pins map[string][]string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		want := pinsFor(pins, cs.ServerName)
		if want == nil {
			return nil
		}
		for _, chain := range cs.VerifiedChains {
			for _, cert := range chain {
				sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
				got := base64.StdEncoding.EncodeToString(sum[:])
				for _, w := range want {
					if got == w {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("tplinkcloud: certificate for %s does not match a pinned key", cs.ServerName)
	}
}

// pinsFor returns the pins for host, honouring one-label "*." wildcards.
func pinsFor(pins map[string][]string, host string) []string {
	var out []string
	for pattern, p := range pins {
		if pattern == host {
			out = append(out, p...)
			continue
		}
		if rest, ok := strings.CutPrefix(pattern, "*."); ok {
			if label, tail, found := strings.Cut(host, "."); found && label != "" && tail == rest {
				out = append(out, p...)
			}
		}
	}
	return out
}

type envelope struct {
	ErrorCode int             `json:"error_code"`
	Msg       string          `json:"msg"`
	Result    json.RawMessage `json:"result"`
}

// post sends a signed JSON POST to base+path and decodes result into out.
// A non-empty token is added as the token query parameter. The result
// is decoded even when error_code is non-zero (MFA challenges carry data).
func (c *Client) post(ctx context.Context, base, path string, body, out any, token string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil {
		return fmt.Errorf("tplinkcloud: bad server URL: %w", err)
	}
	if u.Scheme != "https" && !c.opts.AllowHTTP {
		return fmt.Errorf("tplinkcloud: refusing non-https server URL %q", u.Host)
	}
	q := u.Query()
	q.Set("appName", AppType)
	q.Set("appVer", c.opts.AppVersion)
	q.Set("netType", "wifi")
	q.Set("termID", c.opts.TerminalUUID)
	q.Set("ospf", "Android 14")
	q.Set("brand", "TPLINK")
	q.Set("locale", c.opts.Locale)
	if token != "" {
		q.Set("token", token)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("App-Type", AppType)
	req.Header.Set("App-Version", c.opts.AppVersion)
	req.Header.Set("Terminal-Id", c.opts.TerminalUUID)
	req.Header.Set("Content-MD5", contentMD5(raw))
	req.Header.Set("X-Authorization", c.opts.Keys.signature(raw, NewTerminalUUID(), u.EscapedPath()))

	resp, err := c.httpc.Do(req)
	if err != nil {
		return redact(err, token)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return redact(err, token)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tplinkcloud: %s: HTTP %d", path, resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("tplinkcloud: %s: malformed response: %w", path, err)
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("tplinkcloud: %s: malformed result: %w", path, err)
		}
	}
	if env.ErrorCode != 0 {
		return &APIError{Code: env.ErrorCode, Msg: env.Msg}
	}
	return nil
}

var tokenRE = regexp.MustCompile(`token=[^&\s"]+`)

// redact removes the token (and anything that looks like one) from err.
func redact(err error, token string) error {
	msg := tokenRE.ReplaceAllString(err.Error(), "token=[redacted]")
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[redacted]")
	}
	return &redactedError{msg: msg, err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }

// Unwrap exposes only the error kind (timeouts, cancellation), never the
// message, which may carry the token.
func (e *redactedError) Unwrap() error {
	switch {
	case errors.Is(e.err, context.Canceled):
		return context.Canceled
	case errors.Is(e.err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	return nil
}
