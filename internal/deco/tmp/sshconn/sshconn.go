// Package sshconn opens the Deco app's LAN transport: SSH to the Deco with
// the TP-Link ID, then a direct-tcpip channel to the on-device TMP service
// at 127.0.0.1:20002 (docs/tmp-protocol.md, "SSH2 transport").
package sshconn

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/t0mer/atlantis/internal/deco"
)

const (
	tmpHost = "127.0.0.1"
	tmpPort = 20002

	appBanner = "SSH-2.0-JSCH-0.1.54"
	tpsBanner = "TPS-2.0-JSCH-0.1.54"
)

var (
	// ErrAuthFailed means the Deco rejected the TP-Link ID credentials. Do
	// not retry: the Deco counts failures and locks out.
	ErrAuthFailed = fmt.Errorf("sshconn: the Deco rejected the TP-Link ID credentials: %w", deco.ErrWrongPassword)
	// ErrHostKeyChanged means the host key differs from the pinned one.
	ErrHostKeyChanged = fmt.Errorf("sshconn: SSH host key changed: %w", deco.ErrCertChanged)
	// ErrUnknownHostKey means no key is pinned and AcceptNew is false. The
	// password is not sent.
	ErrUnknownHostKey = fmt.Errorf("sshconn: unknown SSH host key: %w", deco.ErrCertUnknown)
)

// Options configure Dial.
type Options struct {
	Host     string
	Port     int // the TDP-reported port; see tdp.Info.SSHPort
	User     string
	Password string
	// HostKeyPin is the expected "SHA256:..." fingerprint. When empty the
	// connection fails closed with ErrUnknownHostKey before any password is
	// sent, unless AcceptNew is set. OnHostKey is told the fingerprint seen
	// whenever no pin was given, so the caller can show or store it.
	HostKeyPin string
	AcceptNew  bool
	OnHostKey  func(fingerprint string)
	// TPSBanner sends the app's TPS banner (TDP banner_type 1).
	TPSBanner bool
	Timeout   time.Duration // connect + handshake; default 15s
}

// Username returns the SSH username for a TP-Link ID: the email, or its
// SHA-1 hex when TDP reports need_account_digest (j0.java). The email is
// lowercased first unless raw is set (unverified which the Deco expects).
func Username(email string, digest, raw bool) string {
	if !raw {
		email = strings.ToLower(strings.TrimSpace(email))
	}
	if !digest {
		return email
	}
	sum := sha1.Sum([]byte(email))
	return hex.EncodeToString(sum[:])
}

// Fingerprint formats a host key like OpenSSH: "SHA256:<base64>".
func Fingerprint(k ssh.PublicKey) string {
	sum := sha256.Sum256(k.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Conn is the TMP byte stream over SSH.
type Conn struct {
	client  *ssh.Client
	channel ssh.Channel
}

func (c *Conn) Read(p []byte) (int, error)  { return c.channel.Read(p) }
func (c *Conn) Write(p []byte) (int, error) { return c.channel.Write(p) }

// Close closes the channel and the SSH connection.
func (c *Conn) Close() error {
	c.channel.Close()
	return c.client.Close()
}

var _ io.ReadWriteCloser = (*Conn)(nil)

// config returns the client config. The algorithm lists add the legacy
// ones the app's JSch 0.1.54 negotiates (ssh-rsa host keys, SHA-1 DH
// groups, CBC ciphers) after Go's defaults, so modern ones win when the
// Deco offers them. Authentication is password only: one attempt.
func config(o Options, hostKey ssh.HostKeyCallback) *ssh.ClientConfig {
	def := ssh.SupportedAlgorithms()
	ins := ssh.InsecureAlgorithms()
	banner := appBanner
	if o.TPSBanner {
		banner = tpsBanner
	}
	return &ssh.ClientConfig{
		User:              o.User,
		Auth:              []ssh.AuthMethod{ssh.Password(o.Password)},
		HostKeyCallback:   hostKey,
		HostKeyAlgorithms: append(append([]string(nil), def.HostKeys...), ssh.KeyAlgoRSA),
		ClientVersion:     banner,
		Timeout:           o.Timeout,
		Config: ssh.Config{
			KeyExchanges: append(append([]string(nil), def.KeyExchanges...), ins.KeyExchanges...),
			Ciphers:      append(append([]string(nil), def.Ciphers...), ins.Ciphers...),
			MACs:         append(append([]string(nil), def.MACs...), ins.MACs...),
		},
	}
}

// Dial connects, authenticates once and opens the TMP channel.
func Dial(ctx context.Context, o Options) (*Conn, error) {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	if o.Port == 0 {
		o.Port = 22
	}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	var seen string
	hostKey := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		seen = Fingerprint(key)
		if o.HostKeyPin == "" {
			if o.OnHostKey != nil {
				o.OnHostKey(seen)
			}
			if !o.AcceptNew {
				return ErrUnknownHostKey
			}
			return nil
		}
		if seen != o.HostKeyPin {
			return ErrHostKeyChanged
		}
		return nil
	}

	d := net.Dialer{Timeout: o.Timeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sshconn: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	} else {
		nc.SetDeadline(time.Now().Add(o.Timeout))
	}
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, config(o, hostKey))
	if err != nil {
		nc.Close()
		return nil, classify(err, o.Password)
	}
	nc.SetDeadline(time.Time{})
	client := ssh.NewClient(sc, chans, reqs)

	ch, chReqs, err := client.OpenChannel("direct-tcpip", directTCPIP(tmpHost, tmpPort, "127.0.0.1", 0))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sshconn: opening the TMP channel (%s:%d): %w", tmpHost, tmpPort, err)
	}
	go ssh.DiscardRequests(chReqs)
	return &Conn{client: client, channel: ch}, nil
}

// directTCPIP is the RFC 4254 §7.2 channel payload.
func directTCPIP(host string, port int, origHost string, origPort int) []byte {
	return ssh.Marshal(struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}{host, uint32(port), origHost, uint32(origPort)})
}

// classify maps handshake errors and scrubs the password from messages.
func classify(err error, password string) error {
	msg := err.Error()
	if password != "" {
		msg = strings.ReplaceAll(msg, password, "[redacted]")
	}
	switch {
	case errors.Is(err, ErrHostKeyChanged):
		return ErrHostKeyChanged
	case errors.Is(err, ErrUnknownHostKey):
		return ErrUnknownHostKey
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("%w (%s)", ErrAuthFailed, msg)
	case strings.Contains(msg, "insecure") && strings.Contains(msg, "rsa"):
		return fmt.Errorf("sshconn: the Deco's RSA host key is below Go's minimum size; "+
			"run with GODEBUG=rsa1024min=0 to allow it (%s)", msg)
	}
	return fmt.Errorf("sshconn: %s", msg)
}
