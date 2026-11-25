// Package tmp implements TP-Link's TMP protocol and its AppV2 business layer,
// which the Deco app speaks over SSH, TLS and the cloud relay. The wire format
// was taken from the Deco Android app (docs/tmp-protocol.md §4). All integers
// are big-endian.
package tmp

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// TMP control codes (vk/a.java).
const (
	CtrlAssocRequest = 1
	CtrlAssocAccept  = 2
	CtrlAssocRefuse  = 3
	CtrlHeartbeat    = 4
	CtrlData         = 5
	CtrlBye          = 6
)

const (
	tmpMainVersion   = 1
	tmpSecondVersion = 1

	baseHeaderLen = 4
	dataHeaderLen = 16
	maxPayload    = 0xFFFF

	// crcPlaceholder sits in the CRC field while the CRC is computed
	// (vk/b.java f129834s).
	crcPlaceholder = 0x5A6B7C8D

	flagCompressed = 1
)

// Frame is one TMP packet. Only data frames (CtrlData) carry the 16-byte
// header and a payload; association, heartbeat and bye frames are 4 bytes.
type Frame struct {
	Ctrl    byte
	Reason  byte // bye reason on CtrlBye
	Err     byte
	Seq     uint32
	Payload []byte // decompressed
}

// ErrCRC means a received data frame failed its checksum.
var ErrCRC = errors.New("tmp: frame checksum mismatch")

// hasDataHeader reports whether ctrl uses the 16-byte header (TMP-V1: data
// only; v1/b.java).
func hasDataHeader(ctrl byte) bool { return ctrl == CtrlData }

// Marshal encodes f. Payloads are sent uncompressed.
func (f Frame) Marshal() ([]byte, error) {
	base := []byte{tmpMainVersion, tmpSecondVersion, f.Ctrl, f.Reason}
	if !hasDataHeader(f.Ctrl) {
		if len(f.Payload) > 0 {
			return nil, fmt.Errorf("tmp: control %d frame cannot carry a payload", f.Ctrl)
		}
		return base, nil
	}
	if len(f.Payload) > maxPayload {
		return nil, fmt.Errorf("tmp: payload of %d bytes exceeds %d", len(f.Payload), maxPayload)
	}
	b := make([]byte, dataHeaderLen, dataHeaderLen+len(f.Payload))
	copy(b, base)
	binary.BigEndian.PutUint16(b[4:], uint16(len(f.Payload)))
	b[6] = 0 // flag: uncompressed
	b[7] = f.Err
	binary.BigEndian.PutUint32(b[8:], f.Seq)
	b = append(b, f.Payload...)
	binary.BigEndian.PutUint32(b[12:], frameCRC(b))
	return b, nil
}

// frameCRC is CRC-32 (IEEE) over the whole frame with the CRC field set to
// the placeholder.
func frameCRC(frame []byte) uint32 {
	h := crc32.NewIEEE()
	h.Write(frame[:12])
	var ph [4]byte
	binary.BigEndian.PutUint32(ph[:], crcPlaceholder)
	h.Write(ph[:])
	h.Write(frame[16:])
	return h.Sum32()
}

// ReadFrame reads one frame from r, verifying the checksum and inflating a
// compressed payload.
func ReadFrame(r io.Reader) (Frame, error) {
	var base [baseHeaderLen]byte
	if _, err := io.ReadFull(r, base[:]); err != nil {
		return Frame{}, err
	}
	if base[0] != tmpMainVersion || base[1] != tmpSecondVersion {
		return Frame{}, fmt.Errorf("tmp: unsupported version %d.%d", base[0], base[1])
	}
	f := Frame{Ctrl: base[2], Reason: base[3]}
	if !hasDataHeader(f.Ctrl) {
		return f, nil
	}
	b := make([]byte, dataHeaderLen)
	copy(b, base[:])
	if _, err := io.ReadFull(r, b[4:]); err != nil {
		return Frame{}, err
	}
	plen := int(binary.BigEndian.Uint16(b[4:]))
	b = append(b, make([]byte, plen)...)
	if _, err := io.ReadFull(r, b[dataHeaderLen:]); err != nil {
		return Frame{}, err
	}
	if binary.BigEndian.Uint32(b[12:]) != frameCRC(b) {
		return Frame{}, ErrCRC
	}
	f.Err = b[7]
	f.Seq = binary.BigEndian.Uint32(b[8:])
	f.Payload = b[dataHeaderLen:]
	if b[6]&flagCompressed != 0 {
		zr, err := zlib.NewReader(bytes.NewReader(f.Payload))
		if err != nil {
			return Frame{}, fmt.Errorf("tmp: compressed payload: %w", err)
		}
		defer zr.Close()
		// A compressed payload expands to at most one AppV2 fragment plus header.
		out, err := io.ReadAll(io.LimitReader(zr, 1<<20))
		if err != nil {
			return Frame{}, fmt.Errorf("tmp: compressed payload: %w", err)
		}
		f.Payload = out
	}
	return f, nil
}
