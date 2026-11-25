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
	def       *stream // the stream Feed and Relay use
	results   map[tmp.Opcode]any
	seen      []tmp.Opcode
	requests  map[tmp.Opcode]json.RawMessage
	freed     bool
	byeAfter  tmp.Opcode
	expireOne bool
	stall     tmp.Opcode
	relayFail bool
	ignoreSet int // WIRELESS_SETs to acknowledge without applying
	assocReqs int
}

// stream is one client connection's protocol state. Transaction IDs and
// the token are per connection, as on the device, so concurrent or
// successive sessions never see each other's replies.
type stream struct {
	buf     []byte
	pending map[uint16][]byte // pushed bodies by transaction
	replies map[uint16][]byte // reply bodies by transaction
	tokenOK bool
}

func newStream() *stream {
	return &stream{pending: map[uint16][]byte{}, replies: map[uint16][]byte{}}
}

// Associations counts version-association requests received.
func (d *Device) Associations() int { d.mu.Lock(); defer d.mu.Unlock(); return d.assocReqs }

// FailRelayOnce makes the next relay TRANSFER fail with "device not
// connected", which ends the client's relay connection.
func (d *Device) FailRelayOnce() { d.mu.Lock(); d.relayFail = true; d.mu.Unlock() }

// Stall makes the device never answer pulls for op.
func (d *Device) Stall(op tmp.Opcode) { d.mu.Lock(); d.stall = op; d.mu.Unlock() }

// New returns a device with default replies for the read opcodes.
func New() *Device {
	return &Device{
		def:      newStream(),
		requests: map[tmp.Opcode]json.RawMessage{},
		results: map[tmp.Opcode]any{
			tmp.OpCompNegotiate:      map[string]any{"component_list": []any{}, "operation_mode": "Router"},
			tmp.OpClientListGet:      map[string]any{"client_list": []any{}},
			tmp.OpClientListSpeedGet: map[string]any{"client_list_speed": []any{}},
			tmp.OpWirelessGet: map[string]any{"band2_4": map[string]any{
				"host": map[string]any{"enable": true, "ssid": "SG9tZQ==", "password": "c2VjcmV0"}}},
			tmp.OpDeviceListGet: map[string]any{"device_list": []any{map[string]any{
				"device_id": "dev-1", "mac": "AA-BB-CC-DD-EE-01", "nickname": "custom", "custom_nickname": "TGl2aW5n", "role": "master",
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

// IgnoreWirelessSets makes the next n WIRELESS_SETs succeed without
// changing anything (a device that does not apply a change).
func (d *Device) IgnoreWirelessSets(n int) { d.mu.Lock(); d.ignoreSet = n; d.mu.Unlock() }

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
	st := d.def
	d.mu.Unlock()
	return d.feed(st, b)
}

func (d *Device) feed(st *stream, b []byte) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	st.buf = append(st.buf, b...)
	var out bytes.Buffer
	for {
		r := bytes.NewReader(st.buf)
		f, err := tmp.ReadFrame(r)
		if err != nil {
			return out.Bytes() // incomplete frame: wait for more
		}
		st.buf = st.buf[len(st.buf)-r.Len():]
		d.handle(st, f, &out)
	}
}

func (d *Device) frame(out *bytes.Buffer, f tmp.Frame) {
	b, _ := f.Marshal()
	out.Write(b)
}

func (d *Device) packet(out *bytes.Buffer, seq uint32, p tmp.Packet) {
	d.frame(out, tmp.Frame{Ctrl: tmp.CtrlData, Seq: seq, Payload: p.Marshal()})
}

func (d *Device) handle(st *stream, f tmp.Frame, out *bytes.Buffer) {
	switch f.Ctrl {
	case tmp.CtrlHeartbeat:
		d.frame(out, tmp.Frame{Ctrl: tmp.CtrlHeartbeat})
		return
	case tmp.CtrlAssocRequest:
		// A new TMP session on this stream: transaction IDs and the token
		// start over.
		d.assocReqs++
		st.pending, st.replies, st.tokenOK = map[uint16][]byte{}, map[uint16][]byte{}, false
		d.frame(out, tmp.Frame{Ctrl: tmp.CtrlAssocAccept})
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
		st.pending[p.TransID] = append(st.pending[p.TransID], p.Data...)
		d.packet(out, f.Seq, tmp.Packet{Op: p.Op, Type: tmp.PacketPushAck, TransID: p.TransID,
			BodyCRC: p.BodyCRC, Total: p.Total, Offset: uint32(len(st.pending[p.TransID]))})
	case tmp.PacketPull:
		if p.Op == d.stall {
			return
		}
		reply, ok := st.replies[p.TransID]
		var errByte byte
		if !ok {
			reply, errByte = d.execute(st, p.Op, st.pending[p.TransID])
			delete(st.pending, p.TransID)
			st.replies[p.TransID] = reply
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

func (d *Device) execute(st *stream, op tmp.Opcode, body []byte) ([]byte, byte) {
	switch op {
	case tmp.OpTokenAlloc:
		st.tokenOK = true
		return []byte(Token), 0
	case tmp.OpTokenFree:
		d.freed = string(body) == Token
		return nil, 0
	}
	if !st.tokenOK {
		return nil, 10
	}
	if d.expireOne {
		d.expireOne, st.tokenOK = false, false
		return nil, 10
	}
	d.seen = append(d.seen, op)
	d.requests[op] = append(json.RawMessage(nil), body...)
	if op == tmp.OpWirelessSet {
		code := 0
		if d.ignoreSet > 0 {
			d.ignoreSet--
		} else if !d.applyWirelessSet(body) {
			code = -1
		}
		b, _ := json.Marshal(map[string]any{"error_code": code, "result": map[string]any{}})
		return b, 0
	}
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
	st := newStream()
	buf := make([]byte, 64<<10)
	for {
		n, err := rw.Read(buf)
		if n > 0 {
			if out := d.feed(st, buf[:n]); len(out) > 0 {
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
		d.mu.Lock()
		fail := d.relayFail
		d.relayFail = false
		d.mu.Unlock()
		if fail {
			d.mu.Lock()
			d.def = newStream() // the relay session is gone
			d.mu.Unlock()
			return map[string]any{"err_code": 4}, nil
		}
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

// applyWirelessSet merges a WIRELESS_SET's params into the WIRELESS_GET
// result. Like a strict device, it rejects the whole change (false) if it
// names a band, network or field the current settings do not have.
func (d *Device) applyWirelessSet(body []byte) bool {
	var req struct {
		Params map[string]map[string]map[string]any `json:"params"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	cur, _ := d.results[tmp.OpWirelessGet].(map[string]any)
	for band, nets := range req.Params {
		b, _ := cur[band].(map[string]any)
		for name, fields := range nets {
			n, _ := b[name].(map[string]any)
			if n == nil {
				return false
			}
			for k := range fields {
				if _, ok := n[k]; !ok {
					return false
				}
			}
		}
	}
	for band, nets := range req.Params {
		b := cur[band].(map[string]any)
		for name, fields := range nets {
			n := b[name].(map[string]any)
			for k, v := range fields {
				n[k] = v
			}
		}
	}
	return true
}
