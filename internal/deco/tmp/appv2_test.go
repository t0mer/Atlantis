package tmp

import (
	"bytes"
	"errors"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	in := Packet{Op: OpDeviceListGet, Type: PacketPush, Err: 0, TransID: 0x1234,
		BodyCRC: 0x561bacaf, Total: 7, Offset: 0, Data: []byte(`{"a":1}`)}
	b := in.Marshal()
	if !bytes.Equal(b[:4], []byte{1, 2, 0x40, 0x0F}) || len(b) != 27 {
		t.Fatalf("header wrong: %x", b)
	}
	out, err := ParsePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.Op != in.Op || out.TransID != in.TransID || out.BodyCRC != in.BodyCRC || string(out.Data) != `{"a":1}` {
		t.Fatalf("round trip: %+v", out)
	}
}

// zlib.crc32(b'{"a":1}') == 0x561bacaf, computed with Python.
func TestFragmentKnownCRC(t *testing.T) {
	ps := Fragment(OpDeviceListGet, PacketPush, 1, []byte(`{"a":1}`))
	if len(ps) != 1 || ps[0].BodyCRC != 0x561bacaf || ps[0].Total != 7 {
		t.Fatalf("%+v", ps)
	}
}

func TestFragmentAndReassembleLargeBody(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 2*MaxFragment/10+77) // > 2 fragments
	ps := Fragment(OpClientListGet, PacketPullAck, 9, body)
	if len(ps) != 3 {
		t.Fatalf("want 3 fragments, got %d", len(ps))
	}
	var a Assembler
	for i, p := range ps {
		p2, err := ParsePacket(p.Marshal())
		if err != nil {
			t.Fatal(err)
		}
		done, err := a.Add(p2)
		if err != nil {
			t.Fatal(err)
		}
		if done != (i == len(ps)-1) {
			t.Fatalf("fragment %d: done=%v", i, done)
		}
	}
	if _, got := a.Body(); !bytes.Equal(got, body) {
		t.Fatal("reassembled body differs")
	}
}

func TestReassembleRejectsCorruption(t *testing.T) {
	ps := Fragment(OpClientListGet, PacketPullAck, 9, bytes.Repeat([]byte("x"), MaxFragment+5))
	ps[1].Data = []byte("yyyyy")
	var a Assembler
	a.Add(ps[0])
	if _, err := a.Add(ps[1]); !errors.Is(err, ErrBodyCRC) {
		t.Fatalf("want ErrBodyCRC, got %v", err)
	}

	var b Assembler
	b.Add(ps[0])
	if _, err := b.Add(ps[0]); err == nil {
		t.Fatal("a repeated offset must be rejected")
	}
}

func TestEmptyBodyIsOnePacket(t *testing.T) {
	ps := Fragment(OpTokenAlloc, PacketPush, 1, nil)
	var a Assembler
	done, err := a.Add(ps[0])
	if len(ps) != 1 || !done || err != nil {
		t.Fatalf("%d %v %v", len(ps), done, err)
	}
}

func TestAllowlist(t *testing.T) {
	if !Allowed(OpDeviceListGet) || !Allowed(OpTokenAlloc) {
		t.Fatal("read opcodes must be allowed")
	}
	for _, op := range []Opcode{0x4008 /* WIRELESS_SET */, 0x4016 /* DEVICE_LIST_REBOOT */, 0x401D /* FW_UPDATE */, 0x43EB /* CONFIG_BACKUP_SET */, 0xC456, 0x00F0} {
		if Allowed(op) {
			t.Errorf("%s must not be allowed", op)
		}
	}
}
