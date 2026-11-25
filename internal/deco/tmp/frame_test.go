package tmp

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

// Vector computed with Python's zlib.crc32 following the app's algorithm:
// CRC over header+payload with the CRC field set to 0x5A6B7C8D.
const vectorHex = "010105000007000000000007905cef57010268656c6c6f"

func TestFrameMarshalKnownAnswer(t *testing.T) {
	b, err := Frame{Ctrl: CtrlData, Seq: 7, Payload: []byte("\x01\x02hello")}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != vectorHex {
		t.Fatalf("got  %s\nwant %s", got, vectorHex)
	}
}

func TestReadFrameRoundTrip(t *testing.T) {
	raw, _ := hex.DecodeString(vectorHex)
	f, err := ReadFrame(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if f.Ctrl != CtrlData || f.Seq != 7 || string(f.Payload) != "\x01\x02hello" {
		t.Fatalf("decoded %+v", f)
	}
}

func TestReadFrameRejectsBadCRC(t *testing.T) {
	raw, _ := hex.DecodeString(vectorHex)
	raw[len(raw)-1] ^= 1
	if _, err := ReadFrame(bytes.NewReader(raw)); !errors.Is(err, ErrCRC) {
		t.Fatalf("want ErrCRC, got %v", err)
	}
}

func TestControlFramesAreFourBytes(t *testing.T) {
	for _, ctrl := range []byte{CtrlAssocRequest, CtrlHeartbeat, CtrlBye} {
		b, err := Frame{Ctrl: ctrl, Reason: 3}.Marshal()
		if err != nil || len(b) != 4 || b[2] != ctrl || b[3] != 3 {
			t.Fatalf("ctrl %d: %x %v", ctrl, b, err)
		}
		f, err := ReadFrame(bytes.NewReader(b))
		if err != nil || f.Ctrl != ctrl || f.Reason != 3 {
			t.Fatalf("ctrl %d decode: %+v %v", ctrl, f, err)
		}
	}
	if _, err := (Frame{Ctrl: CtrlBye, Payload: []byte{1}}).Marshal(); err == nil {
		t.Fatal("a control frame with a payload must be rejected")
	}
}

func TestReadFrameInflatesCompressedPayload(t *testing.T) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write([]byte("inflated body"))
	w.Close()
	b := make([]byte, 16)
	copy(b, []byte{1, 1, CtrlData, 0})
	binary.BigEndian.PutUint16(b[4:], uint16(z.Len()))
	b[6] = flagCompressed
	binary.BigEndian.PutUint32(b[8:], 9)
	b = append(b, z.Bytes()...)
	binary.BigEndian.PutUint32(b[12:], frameCRC(b))
	f, err := ReadFrame(bytes.NewReader(b))
	if err != nil || string(f.Payload) != "inflated body" {
		t.Fatalf("%q %v", f.Payload, err)
	}
}
