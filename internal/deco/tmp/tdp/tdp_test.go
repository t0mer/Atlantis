package tdp

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"net"
	"strings"
	"testing"
)

// The request layout from tdp/packet/b.java, with the checksum computed the
// same way as the TMP frame CRC (placeholder in the field).
func TestRequestLayout(t *testing.T) {
	b := request(0x01020304)
	if got := hex.EncodeToString(b[:12]); got != "020000010000210001020304" {
		t.Fatalf("header %s", got)
	}
	want := crc32.ChecksumIEEE(append(append([]byte(nil), b[:12]...), 0x5A, 0x6B, 0x7C, 0x8D))
	if binary.BigEndian.Uint32(b[12:]) != want {
		t.Fatal("checksum not computed over the placeholder")
	}
}

func reply(serial uint32, body string) []byte {
	b := make([]byte, headerLen, headerLen+len(body))
	b[0] = version
	binary.BigEndian.PutUint16(b[2:], opcodeDiscover)
	binary.BigEndian.PutUint16(b[4:], uint16(len(body)))
	binary.BigEndian.PutUint32(b[8:], serial)
	b = append(b, body...)
	binary.BigEndian.PutUint32(b[12:], checksum(b))
	return b
}

func TestDiscoverAgainstFakeDevice(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 64)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n != headerLen {
			return
		}
		serial := binary.BigEndian.Uint32(buf[8:])
		pc.WriteTo(reply(serial+1, `{"error_code":0,"result":{}}`), addr) // stray: wrong serial
		pc.WriteTo(reply(serial, `{"error_code":0,"result":{"device_model":"X50","role":"master",
			"tmp_port":[20001],"need_account_digest":true,"banner_type":1}}`), addr)
	}()
	info, err := discover(context.Background(), pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "X50" || info.SSHPort() != 20001 || !info.NeedAccountDigest || !info.TPSBanner() {
		t.Fatalf("%+v", info)
	}
}

func TestSSHPortRules(t *testing.T) {
	var i Info
	if i.SSHPort() != 22 {
		t.Fatal("default must be 22")
	}
	i.TMPPorts = []int{20001}
	if i.SSHPort() != 20001 {
		t.Fatal("tmp_port[0]")
	}
	i.TPAP = &struct {
		Port int `json:"port"`
	}{Port: 443}
	if i.SSHPort() != 443 {
		t.Fatal("tpap.port wins")
	}
}

func TestParseReplyRejectsCorruption(t *testing.T) {
	b := reply(7, `{"error_code":0,"result":{}}`)
	b[len(b)-2] ^= 1
	if _, err := parseReply(b, 7); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("got %v", err)
	}
	if _, err := parseReply(reply(7, `{"error_code":-1}`), 7); err == nil {
		t.Fatal("error_code must be reported")
	}
}
