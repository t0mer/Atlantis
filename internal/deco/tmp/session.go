package tmp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// AppV2 header error codes (business/v2/o.java P()).
const (
	errTokenExpired   = 10
	errTokenAlloc     = 11
	errBusinessFailed = 12
)

var appV2ErrNames = map[byte]string{
	2: "business type", 3: "business version", 4: "operation", 5: "flags", 6: "transaction id",
	7: "transaction crc32", 8: "transaction total bytes", 9: "transaction start byte",
	errTokenExpired: "token expired", errTokenAlloc: "token alloc", errBusinessFailed: "business failed",
}

// Bye reasons (v1/b.java).
var byeReasons = map[byte]string{
	0: "no reason", 2: "heartbeat timeout", 3: "client limit", 4: "system reboot",
	5: "wireless down", 6: "system upgrade", 7: "user count exceeded", 8: "auth changed",
	16: "kicked out", 17: "component changed",
}

// ByeError means the device closed the session.
type ByeError struct{ Reason byte }

func (e ByeError) Error() string {
	n := byeReasons[e.Reason]
	if n == "" {
		n = "unknown reason"
	}
	return fmt.Sprintf("tmp: device closed the session (%s)", n)
}

// AppV2Error is a non-zero err byte in a device reply.
type AppV2Error struct {
	Op   Opcode
	Code byte
}

func (e AppV2Error) Error() string {
	n := appV2ErrNames[e.Code]
	if n == "" {
		n = "unknown"
	}
	return fmt.Sprintf("tmp: %s failed: %s error (%d)", e.Op, n, e.Code)
}

// ResultError is a non-zero error_code in a reply body.
type ResultError struct {
	Op   Opcode
	Code int
	Msg  string
}

func (e ResultError) Error() string {
	return fmt.Sprintf("tmp: %s returned error_code %d: %s", e.Op, e.Code, e.Msg)
}

// Options configure a Session.
type Options struct {
	// Username is the TP-Link ID email. COMP_NEGOTIATE sends its SHA-256.
	Username string
	// Owner selects ROLE_OWNER (else ROLE_USER) in COMP_NEGOTIATE.
	Owner bool
	// Heartbeat is the idle heartbeat period; 0 disables it. The app uses
	// 3s on SSH/TLS and none over the cloud relay.
	Heartbeat time.Duration
	// Timeout bounds each request (default 30s, as in the app).
	Timeout time.Duration
	// Associate runs the TMP version association first: send
	// 01 01 01 00, wait for 01 01 02 xx, acknowledge with 01 01 02 00
	// (tmp/h0.java). The app's TMP-V1 layer skips it on some paths.
	Associate bool
}

// Session is an AppV2 session over a TMP byte stream. Requests are
// serialised: the device handles one transaction at a time.
type Session struct {
	conn io.ReadWriteCloser
	opts Options

	mu      sync.Mutex // one transaction at a time; guards fields below
	seq     uint32
	transID uint16
	token   []byte

	wmu      sync.Mutex // serialises frame writes (requests vs heartbeat)
	in       chan Packet
	assoc    chan struct{} // receives on CtrlAssocAccept
	deadCh   chan struct{} // closed on the first fatal error
	deadOnce sync.Once
	closing  chan struct{} // closed when Close starts; aborts waiting requests
	done     chan struct{} // closed when Close has finished with the stream
	closed   sync.Once
	last     time.Time
	lastMu   sync.Mutex // guards last and dead
	dead     error      // first fatal error; the stream is unusable after it
}

// ErrSessionClosed is returned by requests made after or during Close.
var ErrSessionClosed = errors.New("tmp: session closed")

// Open starts a session: TOKEN_ALLOC, then COMP_NEGOTIATE, as the app does
// after the transport connects (manager/repo/a0.java). It returns the
// negotiation result.
func Open(ctx context.Context, conn io.ReadWriteCloser, opts Options) (*Session, json.RawMessage, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	s := &Session{
		conn: conn, opts: opts, in: make(chan Packet, 64),
		deadCh: make(chan struct{}), closing: make(chan struct{}), done: make(chan struct{}),
		assoc: make(chan struct{}, 1),
	}
	go s.readLoop()
	if opts.Heartbeat > 0 {
		go s.heartbeatLoop()
	}
	if opts.Associate {
		if err := s.associate(ctx); err != nil {
			s.Close()
			return nil, nil, err
		}
	}
	if err := s.alloc(ctx); err != nil {
		s.Close()
		return nil, nil, err
	}
	params := map[string]string{"role": "ROLE_USER"}
	if opts.Owner {
		params["role"] = "ROLE_OWNER"
	}
	if opts.Username != "" {
		sum := sha256.Sum256([]byte(opts.Username))
		params["username"] = hex.EncodeToString(sum[:])
	}
	neg, err := s.Request(ctx, OpCompNegotiate, params)
	if err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("negotiating components: %w", err)
	}
	return s, neg, nil
}

func (s *Session) associate(ctx context.Context) error {
	if err := s.writeFrame(Frame{Ctrl: CtrlAssocRequest}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	select {
	case <-s.assoc:
	case <-s.deadCh:
		return s.deadErr()
	case <-ctx.Done():
		return fmt.Errorf("tmp: version association: %w", ctx.Err())
	}
	return s.writeFrame(Frame{Ctrl: CtrlAssocAccept})
}

func (s *Session) alloc(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, body, err := s.transact(ctx, OpTokenAlloc, nil, s.closing)
	if err != nil {
		return fmt.Errorf("allocating session token: %w", err)
	}
	if len(body) == 0 {
		return errors.New("tmp: TOKEN_ALLOC returned no token")
	}
	s.token = body
	return nil
}

// Request runs one allowlisted business operation. params may be nil. It
// returns the "result" member of the reply.
func (s *Session) Request(ctx context.Context, op Opcode, params any) (json.RawMessage, error) {
	if !Allowed(op) || op == OpTokenAlloc || op == OpTokenVerify || op == OpTokenFree {
		return nil, ErrOpcodeNotAllowed{Op: op}
	}
	req := struct {
		ConfigVersion int64 `json:"config_version"`
		Params        any   `json:"params,omitempty"`
	}{time.Now().UnixMilli(), params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closing:
		return nil, ErrSessionClosed
	default:
	}
	_, reply, err := s.transact(ctx, op, body, s.closing)
	var ae AppV2Error
	if errors.As(err, &ae) && ae.Code == errTokenExpired {
		// The device dropped our token: allocate a new one and retry once.
		if _, tok, aerr := s.transact(ctx, OpTokenAlloc, nil, s.closing); aerr == nil && len(tok) > 0 {
			s.token = tok
			_, reply, err = s.transact(ctx, op, body, s.closing)
		}
	}
	if err != nil {
		return nil, err
	}
	var res struct {
		ErrorCode int             `json:"error_code"`
		Msg       string          `json:"msg"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(reply, &res); err != nil {
		return nil, fmt.Errorf("tmp: %s: malformed reply: %w", op, err)
	}
	if res.ErrorCode != 0 {
		return nil, ResultError{Op: op, Code: res.ErrorCode, Msg: res.Msg}
	}
	return res.Result, nil
}

// transact pushes body as op and pulls the reply (business/v2/transaction).
// It gives up when abort is closed. Caller holds s.mu.
func (s *Session) transact(ctx context.Context, op Opcode, body []byte, abort <-chan struct{}) (Packet, []byte, error) {
	if !Allowed(op) {
		return Packet{}, nil, ErrOpcodeNotAllowed{Op: op}
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	s.transID++
	id := s.transID

	var asm Assembler
	for _, p := range Fragment(op, PacketPush, id, body) {
		if err := s.writePacket(p); err != nil {
			return Packet{}, nil, err
		}
		ack, err := s.await(ctx, op, id, abort)
		if err != nil {
			return Packet{}, nil, err
		}
		if ack.Type == PacketPullAck { // device answered the push directly
			if done, err := asm.Add(ack); err != nil || done {
				h, b := asm.Body()
				return h, b, err
			}
			break
		}
		if ack.Type != PacketPushAck {
			return Packet{}, nil, fmt.Errorf("tmp: %s: unexpected packet type %d", op, ack.Type)
		}
	}
	for {
		_, sofar := asm.Body()
		if err := s.writePacket(Packet{Op: op, Type: PacketPull, TransID: id, Offset: uint32(len(sofar))}); err != nil {
			return Packet{}, nil, err
		}
		p, err := s.await(ctx, op, id, abort)
		if err != nil {
			return Packet{}, nil, err
		}
		if p.Type != PacketPullAck {
			return Packet{}, nil, fmt.Errorf("tmp: %s: unexpected packet type %d", op, p.Type)
		}
		done, err := asm.Add(p)
		if err != nil {
			return Packet{}, nil, err
		}
		if done {
			h, b := asm.Body()
			return h, b, nil
		}
	}
}

// await returns the next reply for transaction id, failing on an err byte.
func (s *Session) await(ctx context.Context, op Opcode, id uint16, abort <-chan struct{}) (Packet, error) {
	for {
		select {
		case <-ctx.Done():
			return Packet{}, fmt.Errorf("tmp: %s: %w", op, ctx.Err())
		case <-abort:
			return Packet{}, ErrSessionClosed
		case <-s.deadCh:
			return Packet{}, s.deadErr()
		case p := <-s.in:
			if p.TransID != id {
				continue // stale reply to an abandoned transaction
			}
			if p.Err != 0 {
				return Packet{}, AppV2Error{Op: op, Code: p.Err}
			}
			return p, nil
		}
	}
}

func (s *Session) writeFrame(f Frame) error {
	if err := s.deadErr(); err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if f.Ctrl == CtrlData {
		s.seq++
		f.Seq = s.seq
	}
	b, err := f.Marshal()
	if err != nil {
		return err
	}
	s.touch()
	if _, err = s.conn.Write(b); err != nil {
		err = fmt.Errorf("tmp: writing: %w", err)
		s.fail(err) // a broken stream is fatal, whichever side notices first
	}
	return err
}

func (s *Session) writePacket(p Packet) error {
	return s.writeFrame(Frame{Ctrl: CtrlData, Payload: p.Marshal()})
}

func (s *Session) touch() {
	s.lastMu.Lock()
	s.last = time.Now()
	s.lastMu.Unlock()
}

func (s *Session) idleFor() time.Duration {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	return time.Since(s.last)
}

func (s *Session) readLoop() {
	for {
		f, err := ReadFrame(s.conn)
		if err != nil {
			s.fail(fmt.Errorf("tmp: reading: %w", err))
			return
		}
		switch f.Ctrl {
		case CtrlHeartbeat:
		case CtrlAssocAccept:
			select {
			case s.assoc <- struct{}{}:
			default:
			}
		case CtrlAssocRefuse:
			s.fail(errors.New("tmp: device refused the session (version association)"))
			return
		case CtrlBye:
			s.fail(ByeError{Reason: f.Reason})
			return
		case CtrlData:
			p, err := ParsePacket(f.Payload)
			if err != nil {
				s.fail(err)
				return
			}
			select {
			case s.in <- p:
			case <-s.done:
				return
			}
		}
	}
}

func (s *Session) heartbeatLoop() {
	t := time.NewTicker(s.opts.Heartbeat / 3)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if s.idleFor() >= s.opts.Heartbeat {
				if err := s.writeFrame(Frame{Ctrl: CtrlHeartbeat}); err != nil {
					s.fail(err)
					return
				}
			}
		}
	}
}

func (s *Session) deadErr() error {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	return s.dead
}

func (s *Session) fail(err error) {
	s.lastMu.Lock()
	if s.dead == nil {
		s.dead = err
	}
	s.lastMu.Unlock()
	s.deadOnce.Do(func() { close(s.deadCh) })
}

// Err returns the error that ended the session, or nil while it is usable.
func (s *Session) Err() error { return s.deadErr() }

// Close frees the token, says bye and closes the transport. After a fatal
// error (bye, broken stream) it only closes the transport.
func (s *Session) Close() error {
	var err error
	s.closed.Do(func() {
		close(s.closing) // abort a waiting request so we get s.mu promptly
		s.mu.Lock()
		if s.token != nil && s.deadErr() == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// The reply is ignored, as in the app (o.java M()).
			s.transact(ctx, OpTokenFree, s.token, nil)
			cancel()
			s.token = nil
		}
		s.mu.Unlock()
		if s.deadErr() == nil {
			s.writeFrame(Frame{Ctrl: CtrlBye})
		}
		close(s.done)
		err = s.conn.Close()
	})
	return err
}
