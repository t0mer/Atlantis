package tmp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// AppV2 packet types (EnumAppV2PacketType).
const (
	PacketPush    = 2
	PacketPushAck = 3
	PacketPull    = 4
	PacketPullAck = 5
)

const (
	appV2HeaderLen = 20
	appV2Start     = 1
	appV2Version   = 2
	// MaxFragment is the largest body slice in one AppV2 packet
	// (business/v2/transaction/a.java f69135k).
	MaxFragment = 8156
	// maxBody bounds reassembly so a peer cannot make us allocate freely.
	maxBody = 8 << 20
)

// Packet is one AppV2 packet: a 20-byte header plus one body fragment.
type Packet struct {
	Op      Opcode
	Type    byte
	Err     byte
	TransID uint16
	BodyCRC uint32 // CRC-32 of the whole (reassembled) body
	Total   uint32 // whole body length
	Offset  uint32 // this fragment's offset (or ack size)
	Data    []byte // this fragment
}

// Marshal encodes p as a TMP data payload.
func (p Packet) Marshal() []byte {
	b := make([]byte, appV2HeaderLen, appV2HeaderLen+len(p.Data))
	b[0], b[1] = appV2Start, appV2Version
	binary.BigEndian.PutUint16(b[2:], uint16(p.Op))
	b[4] = p.Type
	b[5] = p.Err
	binary.BigEndian.PutUint16(b[6:], p.TransID)
	binary.BigEndian.PutUint32(b[8:], p.BodyCRC)
	binary.BigEndian.PutUint32(b[12:], p.Total)
	binary.BigEndian.PutUint32(b[16:], p.Offset)
	return append(b, p.Data...)
}

// ParsePacket decodes a TMP data payload.
func ParsePacket(b []byte) (Packet, error) {
	if len(b) < appV2HeaderLen {
		return Packet{}, fmt.Errorf("tmp: AppV2 packet of %d bytes is too short", len(b))
	}
	if b[0] != appV2Start || b[1] != appV2Version {
		return Packet{}, fmt.Errorf("tmp: not an AppV2 packet (%02x %02x)", b[0], b[1])
	}
	return Packet{
		Op:      Opcode(binary.BigEndian.Uint16(b[2:])),
		Type:    b[4],
		Err:     b[5],
		TransID: binary.BigEndian.Uint16(b[6:]),
		BodyCRC: binary.BigEndian.Uint32(b[8:]),
		Total:   binary.BigEndian.Uint32(b[12:]),
		Offset:  binary.BigEndian.Uint32(b[16:]),
		Data:    b[appV2HeaderLen:],
	}, nil
}

// Fragment splits body into packets of at most MaxFragment bytes, each
// carrying the CRC and length of the whole body. An empty body yields one
// packet with CRC, total and offset all zero.
func Fragment(op Opcode, typ byte, transID uint16, body []byte) []Packet {
	if len(body) == 0 {
		return []Packet{{Op: op, Type: typ, TransID: transID}}
	}
	sum, total := crc32.ChecksumIEEE(body), uint32(len(body))
	var out []Packet
	for off := 0; off < len(body); off += MaxFragment {
		out = append(out, Packet{
			Op: op, Type: typ, TransID: transID, BodyCRC: sum, Total: total,
			Offset: uint32(off), Data: body[off:min(off+MaxFragment, len(body))],
		})
	}
	return out
}

// ErrBodyCRC means a reassembled body failed its checksum
// (ERR_TRANSACTION_ALL_PAYLOAD_CHECKSUM).
var ErrBodyCRC = errors.New("tmp: AppV2 body checksum mismatch")

// Assembler reassembles the fragments of one transaction, in order.
type Assembler struct {
	first Packet
	body  []byte
	began bool
}

// Add appends a fragment. It returns true once the body is complete.
func (a *Assembler) Add(p Packet) (bool, error) {
	if !a.began {
		if p.Total > maxBody {
			return false, fmt.Errorf("tmp: AppV2 body of %d bytes is too large", p.Total)
		}
		a.first, a.began = p, true
		a.body = make([]byte, 0, p.Total)
	}
	switch {
	case p.TransID != a.first.TransID || p.Op != a.first.Op:
		return false, fmt.Errorf("tmp: fragment for %s/%d during %s/%d", p.Op, p.TransID, a.first.Op, a.first.TransID)
	case p.Total != a.first.Total || p.BodyCRC != a.first.BodyCRC:
		return false, errors.New("tmp: fragment total or checksum changed mid-transaction")
	case int(p.Offset) != len(a.body):
		return false, fmt.Errorf("tmp: fragment at offset %d, expected %d", p.Offset, len(a.body))
	case len(a.body)+len(p.Data) > int(a.first.Total):
		return false, errors.New("tmp: fragment overruns the body length")
	}
	a.body = append(a.body, p.Data...)
	if len(a.body) < int(a.first.Total) {
		return false, nil
	}
	if a.first.Total > 0 && crc32.ChecksumIEEE(a.body) != a.first.BodyCRC {
		return false, ErrBodyCRC
	}
	return true, nil
}

// Body returns the reassembled body and the first packet's header fields.
func (a *Assembler) Body() (Packet, []byte) { return a.first, a.body }
