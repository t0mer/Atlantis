// Package fakedeco is an in-process Deco that speaks TMP/AppV2, for tests.
// It answers TOKEN_ALLOC, COMP_NEGOTIATE and a table of canned replies,
// using the same PUSH/PULL flow as the device. It also serves as the device
// behind fakecloud's passthrough relay (see Relay).
package fakedeco

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"sync"

	"github.com/t0mer/atlantis/internal/deco/tmp"
)

// Token is the session token the fake allocates.
const Token = "fake-session-token"

// Device is the fake. Feed it bytes; it returns the bytes it sends back.
type Device struct {
	mu        sync.Mutex
	buf       []byte
	results   map[tmp.Opcode]any
	pending   map[uint16][]byte // pushed bodies by transaction
	replies   map[uint16][]byte // reply bodies by transaction
	seen      []tmp.Opcode
	requests  map[tmp.Opcode]json.RawMessage
	freed     bool
	byeAfter  tmp.Opcode
	expireOne bool
	tokenOK   bool
	stall     tmp.Opcode
}

// Stall makes the device never answer pulls for op.
func (d *Device) Stall(op tmp.Opcode) { d.mu.Lock(); d.stall = op; d.mu.Unlock() }

// New returns a device with default replies for the read opcodes.
func New() *Device {
	return &Device{
		pending:  map[uint16][]byte{},
		replies:  map[uint16][]byte{},
		requests: map[tmp.Opcode]json.RawMessage{},
		results: map[tmp.Opcode]any{
			tmp.OpCompNegotiate: map[string]any{"component_list": []any{}, "operation_mode": "Router"},
			tmp.OpDeviceListGet: map[string]any{"device_list": []any{map[string]any{
				"device_id": "dev-1", "mac": "AA-BB-CC-DD-EE-01", "nickname": "TGl2aW5n", "role": "master",
			}}},
		},
	}
}

// SetResult sets the result returned for op.
func (d *Device) SetResult(op tmp.Opcode, result any) {
	d.mu.Lock()
	d.results[op] = result
	d.mu.Unlock()
}

// ByeAfter makes the device send a bye (client limit) after replying to op.
func (d *Device) ByeAfter(op tmp.Opcode) { d.mu.Lock(); d.byeAfter = op; d.mu.Unlock() }

// ExpireTokenOnce makes the next business request fail with token expired.
func (d *Device) ExpireTokenOnce() { d.mu.Lock(); d.expireOne = true; d.mu.Unlock() }

// Seen lists the opcodes of completed requests, in order.
func (d *Device) Seen() []tmp.Opcode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]tmp.Opcode(nil), d.seen...)
}

// Request returns the last request body for op.
func (d *Device) Request(op tmp.Opcode) json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests[op]
}

// Freed reports whether TOKEN_FREE arrived with the right token.
func (d *Device) Freed() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.freed }

// Feed consumes bytes from the client and returns the bytes to send back.
func (d *Device) Feed(b []byte) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, b...)
	var out bytes.Buffer
	for {
		r := bytes.NewReader(d.buf)
		f, err := tmp.ReadFrame(r)
		if err != nil {
			return out.Bytes() // incomplete frame: wait for more
		}
		d.buf = d.buf[len(d.buf)-r.Len():]
		d.handle(f, &out)
	}
}

func (d *Device) frame(out *bytes.Buffer, f tmp.Frame) {
	b, _ := f.Marshal()
	out.Write(b)
}

func (d *Device) packet(out *bytes.Buffer, seq uint32, p tmp.Packet) {
	d.frame(out, tmp.Frame{Ctrl: tmp.CtrlData, Seq: seq, Payload: p.Marshal()})
}

func (d *Device) handle(f tmp.Frame, out *bytes.Buffer) {
	switch f.Ctrl {
	case tmp.CtrlHeartbeat:
		d.frame(out, tmp.Frame{Ctrl: tmp.CtrlHeartbeat})
		return
	case tmp.CtrlData:
	default:
		return
	}
	p, err := tmp.ParsePacket(f.Payload)
	if err != nil {
		return
	}
	switch p.Type {
	case tmp.PacketPush:
		d.pending[p.TransID] = append(d.pending[p.TransID], p.Data...)
		d.packet(out, f.Seq, tmp.Packet{Op: p.Op, Type: tmp.PacketPushAck, TransID: p.TransID,
			BodyCRC: p.BodyCRC, Total: p.Total, Offset: uint32(len(d.pending[p.TransID]))})
	case tmp.PacketPull:
		if p.Op == d.stall {
			return
		}
		reply, ok := d.replies[p.TransID]
		var errByte byte
		if !ok {
			reply, errByte = d.execute(p.Op, d.pending[p.TransID])
			delete(d.pending, p.TransID)
			d.replies[p.TransID] = reply
		}
		frags := tmp.Fragment(p.Op, tmp.PacketPullAck, p.TransID, reply)
		for _, fr := range frags {
			if fr.Offset == p.Offset {
				fr.Err = errByte
				d.packet(out, f.Seq, fr)
			}
		}
		if p.Op == d.byeAfter {
			d.frame(out, tmp.Frame{Ctrl: tmp.CtrlBye, Reason: 3})
		}
	}
}

func (d *Device) execute(op tmp.Opcode, body []byte) ([]byte, byte) {
	switch op {
	case tmp.OpTokenAlloc:
		d.tokenOK = true
		return []byte(Token), 0
	case tmp.OpTokenFree:
		d.freed = string(body) == Token
		return nil, 0
	}
	if !d.tokenOK {
		return nil, 10
	}
	if d.expireOne {
		d.expireOne, d.tokenOK = false, false
		return nil, 10
	}
	d.seen = append(d.seen, op)
	d.requests[op] = append(json.RawMessage(nil), body...)
	res, ok := d.results[op]
	if !ok {
		b, _ := json.Marshal(map[string]any{"error_code": -40101, "msg": "unsupported"})
		return b, 0
	}
	b, _ := json.Marshal(map[string]any{"error_code": 0, "result": res})
	return b, 0
}

// Serve runs the device on a stream (for net.Pipe tests) until it closes.
func (d *Device) Serve(rw io.ReadWriter) {
	buf := make([]byte, 64<<10)
	for {
		n, err := rw.Read(buf)
		if n > 0 {
			if out := d.Feed(buf[:n]); len(out) > 0 {
				if _, werr := rw.Write(out); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Relay adapts the device to fakecloud's passthrough handler, playing the
// cloud relay's CONNECT / TRANSFER / DISCONNECT protocol.
func (d *Device) Relay(deviceID string, requestData json.RawMessage) (any, error) {
	var req struct {
		Method  string `json:"method"`
		Type    string `json:"type"`
		SN      int64  `json:"sn"`
		RawData string `json:"raw_data"`
	}
	if err := json.Unmarshal(requestData, &req); err != nil {
		return nil, err
	}
	switch req.Method {
	case "TRANSFER":
		in, err := base64.StdEncoding.DecodeString(req.RawData)
		if err != nil {
			return map[string]any{"err_code": 2}, nil
		}
		return map[string]any{"err_code": 0, "sn": req.SN,
			"raw_data": base64.StdEncoding.EncodeToString(d.Feed(in))}, nil
	case "CONNECT":
		if req.Type != "TMP" {
			return map[string]any{"err_code": 4}, nil
		}
	}
	return map[string]any{"err_code": 0}, nil
}
