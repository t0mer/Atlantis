// Package ata tunnels a TMP byte stream to a Deco through the TP-Link cloud
// relay, as the Deco app's "ATA" transport does (docs/tmp-protocol.md §3).
//
// The relay is request/response only: every outbound chunk is a TRANSFER
// carrying base64 bytes, and inbound bytes ride on TRANSFER responses, so the
// connection polls with empty TRANSFERs when it has nothing to send.
package ata

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"sync"
	"time"
)

// Relay sends one passthrough request to a device. *tplinkcloud.Client
// satisfies it.
type Relay interface {
	Passthrough(ctx context.Context, appServerURL, deviceID string, request any) (json.RawMessage, error)
	Token() string
}

// Timing, from com/tplink/ata/s.java and u.java.
const (
	flushDelay     = 150 * time.Millisecond
	maxBatchBase64 = 30720
	minPoll        = 50 * time.Millisecond
	maxPoll        = 15 * time.Second
	pollBackoff    = 2.0
	pollJitter     = 0.25
	requestTimeout = 30 * time.Second
	drainTimeout   = 5 * time.Second
	maxReadBuffer  = 16 << 20
)

// Inner error codes (com/tplink/ata/h.java). 0 and 1 mean success.
var errCodes = map[int]string{
	2: "missing raw data",
	3: "sequence number rejected",
	4: "device not connected",
	5: "relay token expired",
}

// ErrClosed is returned after Close or a fatal relay error.
var ErrClosed = errors.New("ata: connection closed")

// Conn is a TMP byte stream over the relay. Read and Write may be called
// from different goroutines.
type Conn struct {
	relay     Relay
	serverURL string
	deviceID  string
	suffix    int64

	mu      sync.Mutex
	cond    *sync.Cond
	out     []byte    // bytes waiting to be sent
	outAt   time.Time // when out became non-empty
	in      []byte    // bytes received, not yet read
	err     error     // terminal error
	closed  bool
	drain   bool // Close is flushing queued bytes before DISCONNECT
	sn      int64
	kick    chan struct{}
	done    chan struct{}
	stopped chan struct{}
}

type response struct {
	ErrCode int    `json:"err_code"`
	SN      int64  `json:"sn"`
	RawData string `json:"raw_data"`
}

// Dial opens a TMP channel to the device and starts the transfer loop.
func Dial(ctx context.Context, relay Relay, serverURL, deviceID string) (*Conn, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(16))
	if err != nil {
		return nil, err
	}
	c := &Conn{
		relay: relay, serverURL: serverURL, deviceID: deviceID, suffix: n.Int64(),
		kick: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	if _, err := c.send(ctx, map[string]any{"method": "CONNECT", "type": "TMP"}); err != nil {
		return nil, fmt.Errorf("ata: connect: %w", err)
	}
	go c.loop()
	return c, nil
}

// token is the relay token: the account token plus a per-connection suffix
// (ata/context/j.java). The account token is read on every request so a
// cloud re-login is picked up.
func (c *Conn) token() string { return fmt.Sprintf("%s-%d", c.relay.Token(), c.suffix) }

func (c *Conn) send(ctx context.Context, req map[string]any) (*response, error) {
	req["token"] = c.token()
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	raw, err := c.relay.Passthrough(ctx, c.serverURL, c.deviceID, req)
	if err != nil {
		return nil, err
	}
	// responseData is an object; tolerate a JSON-encoded string as well.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("malformed relay response: %w", err)
	}
	if r.ErrCode != 0 && r.ErrCode != 1 {
		msg := errCodes[r.ErrCode]
		if msg == "" {
			msg = "relay error"
		}
		return nil, fmt.Errorf("%s (err_code %d)", msg, r.ErrCode)
	}
	return &r, nil
}

// Write queues p for the next TRANSFER.
func (c *Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return 0, c.termErr()
	}
	if len(c.out) == 0 {
		c.outAt = time.Now()
	}
	c.out = append(c.out, p...)
	select {
	case c.kick <- struct{}{}:
	default:
	}
	return len(p), nil
}

// Read returns received bytes, blocking until some arrive or the
// connection ends.
func (c *Conn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.in) == 0 && !c.closed && c.err == nil {
		c.cond.Wait()
	}
	if len(c.in) == 0 {
		return 0, c.termErr()
	}
	n := copy(p, c.in)
	c.in = c.in[n:]
	return n, nil
}

func (c *Conn) termErr() error {
	if c.err != nil {
		return c.err
	}
	return ErrClosed
}

// Close flushes queued bytes (such as a TMP bye), stops the loop and sends
// DISCONNECT.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed, c.drain = true, true
	c.cond.Broadcast()
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
	select {
	case <-c.stopped: // flushed, or the loop had already failed
	case <-time.After(drainTimeout):
	}
	close(c.done)
	<-c.stopped
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	_, err := c.send(ctx, map[string]any{"method": "DISCONNECT"})
	return err
}

// takeBatch removes up to maxBatchBase64 worth of queued bytes.
func (c *Conn) takeBatch() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(len(c.out), maxBatchBase64/4*3)
	b := append([]byte(nil), c.out[:n]...)
	c.out = c.out[n:]
	if len(c.out) > 0 {
		c.outAt = time.Now()
	}
	return b
}

// due reports whether queued data should go now, and otherwise how long
// until it should.
func (c *Conn) due() (bool, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.out) == 0 {
		return false, -1
	}
	if c.drain || base64.StdEncoding.EncodedLen(len(c.out)) >= maxBatchBase64 {
		return true, 0
	}
	wait := flushDelay - time.Since(c.outAt)
	return wait <= 0, wait
}

// loop sends queued data and polls for inbound data, one request at a time
// so that sn stays ordered.
func (c *Conn) loop() {
	defer close(c.stopped)
	poll := minPoll
	for {
		if c.drained() {
			return
		}
		ready, wait := c.due()
		if !ready {
			d := jitter(poll)
			if wait >= 0 && wait < d {
				d = wait
			}
			t := time.NewTimer(d)
			select {
			case <-c.done:
				t.Stop()
				return
			case <-c.kick:
				t.Stop()
				continue
			case <-t.C:
			}
			if wait >= 0 {
				continue // data is queued: re-check its flush time
			}
			// nothing queued: fall through and poll with an empty TRANSFER
		}
		batch := c.takeBatch()
		c.mu.Lock()
		c.sn++
		sn := c.sn
		c.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-c.done:
				cancel()
			case <-ctx.Done():
			}
		}()
		r, err := c.send(ctx, map[string]any{
			"method": "TRANSFER", "sn": sn, "raw_data": base64.StdEncoding.EncodeToString(batch),
		})
		cancel()
		if err != nil {
			c.fail(err)
			return
		}
		data, err := base64.StdEncoding.DecodeString(r.RawData)
		if err != nil {
			c.fail(fmt.Errorf("ata: bad raw_data: %w", err))
			return
		}
		if len(data) > 0 || len(batch) > 0 {
			poll = minPoll
		} else {
			poll = min(time.Duration(float64(poll)*pollBackoff), maxPoll)
		}
		if len(data) > 0 {
			c.mu.Lock()
			if len(c.in)+len(data) > maxReadBuffer {
				c.mu.Unlock()
				c.fail(errors.New("ata: inbound buffer overflow"))
				return
			}
			c.in = append(c.in, data...)
			c.cond.Broadcast()
			c.mu.Unlock()
		}
	}
}

// drained reports that Close is waiting and nothing is left to send.
func (c *Conn) drained() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.drain && len(c.out) == 0
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed && c.err == nil {
		c.err = fmt.Errorf("ata: %w", err)
	}
	c.cond.Broadcast()
}

func jitter(d time.Duration) time.Duration {
	f := 1 + pollJitter*(2*mrand.Float64()-1)
	return time.Duration(float64(d) * f)
}
