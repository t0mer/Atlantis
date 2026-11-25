// Package tdp implements TP-Link's TDP discovery query for Decos: one UDP
// datagram to port 20002, answered with JSON describing the device,
// including the port of its SSH-based TMP service and how the SSH username
// is formed (docs/tmp-protocol.md, "SSH2 transport").
package tdp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math/big"
	"net"
	"strconv"
	"time"
)

// Port is the TDP UDP port.
const Port = 20002

const (
	headerLen      = 16
	version        = 2
	opcodeDiscover = 1
	flagUnicast    = 0x21
	crcPlaceholder = 0x5A6B7C8D
	maxReply       = 8 << 10
	tryTimeout     = 3 * time.Second
	tries          = 2
)

// Info is the subset of a TDP reply Atlantis uses.
type Info struct {
	IP                string `json:"ip"`
	MAC               string `json:"mac"`
	DeviceID          string `json:"device_id"`
	DeviceType        string `json:"device_type"`
	Model             string `json:"device_model"`
	Role              string `json:"role"`
	FactoryDefault    bool   `json:"factory_default"`
	BannerType        int    `json:"banner_type"`
	SSHBannerType     int    `json:"ssh_banner_type"`
	TMPPorts          []int  `json:"tmp_port"`
	NeedAccountDigest bool   `json:"need_account_digest"`
	TPAP              *struct {
		Port int `json:"port"`
	} `json:"tpap"`

	Raw json.RawMessage `json:"-"` // the whole result object
}

// SSHPort is the port of the SSH-based TMP service: tpap.port, else
// tmp_port[0], else 22 (TDPMeshDevice.java, TDPGenericDevice.java).
func (i Info) SSHPort() int {
	switch {
	case i.TPAP != nil && i.TPAP.Port > 0:
		return i.TPAP.Port
	case len(i.TMPPorts) > 0 && i.TMPPorts[0] > 0:
		return i.TMPPorts[0]
	}
	return 22
}

// TPSBanner reports whether the device expects the app's TPS SSH banner.
func (i Info) TPSBanner() bool { return i.BannerType == 1 || i.SSHBannerType == 1 }

// checksum is CRC-32 (IEEE) over the packet with the checksum field set to
// the placeholder.
func checksum(pkt []byte) uint32 {
	b := append([]byte(nil), pkt...)
	binary.BigEndian.PutUint32(b[12:], crcPlaceholder)
	return crc32.ChecksumIEEE(b)
}

func request(serial uint32) []byte {
	b := make([]byte, headerLen)
	b[0] = version
	binary.BigEndian.PutUint16(b[2:], opcodeDiscover)
	b[6] = flagUnicast
	binary.BigEndian.PutUint32(b[8:], serial)
	binary.BigEndian.PutUint32(b[12:], checksum(b))
	return b
}

// parseReply checks a reply against the request serial and decodes it.
func parseReply(b []byte, serial uint32) (Info, error) {
	if len(b) < headerLen {
		return Info{}, errors.New("tdp: short reply")
	}
	if b[0] != version || binary.BigEndian.Uint32(b[8:]) != serial {
		return Info{}, errors.New("tdp: reply does not match the request")
	}
	if b[7] > 1 {
		return Info{}, fmt.Errorf("tdp: device returned result %d", b[7])
	}
	if binary.BigEndian.Uint32(b[12:]) != checksum(b) {
		return Info{}, errors.New("tdp: reply checksum mismatch")
	}
	n := int(binary.BigEndian.Uint16(b[4:]))
	if headerLen+n > len(b) {
		return Info{}, errors.New("tdp: truncated reply")
	}
	var env struct {
		ErrorCode int             `json:"error_code"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b[headerLen:headerLen+n], &env); err != nil {
		return Info{}, fmt.Errorf("tdp: reply is not JSON: %w", err)
	}
	if env.ErrorCode != 0 {
		return Info{}, fmt.Errorf("tdp: device returned error_code %d", env.ErrorCode)
	}
	var info Info
	if err := json.Unmarshal(env.Result, &info); err != nil {
		return Info{}, fmt.Errorf("tdp: bad result: %w", err)
	}
	info.Raw = env.Result
	return info, nil
}

// Discover queries the Deco at host (an IP or name, without port).
func Discover(ctx context.Context, host string) (Info, error) {
	return discover(ctx, net.JoinHostPort(host, strconv.Itoa(Port)))
}

func discover(ctx context.Context, addr string) (Info, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return Info{}, fmt.Errorf("tdp: %w", err)
	}
	defer conn.Close()
	n, err := rand.Int(rand.Reader, big.NewInt(1<<28))
	if err != nil {
		return Info{}, err
	}
	serial := uint32(n.Int64())
	buf := make([]byte, maxReply)
	var last error = errors.New("tdp: no reply")
	for range tries {
		if _, err := conn.Write(request(serial)); err != nil {
			return Info{}, fmt.Errorf("tdp: %w", err)
		}
		deadline := time.Now().Add(tryTimeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		conn.SetReadDeadline(deadline)
		for {
			m, err := conn.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return Info{}, ctx.Err()
				}
				break // timeout: send again
			}
			info, err := parseReply(buf[:m], serial)
			if err == nil {
				return info, nil
			}
			last = err // ignore stray datagrams, keep waiting
		}
	}
	return Info{}, fmt.Errorf("tdp: no valid reply from %s: %w", addr, last)
}
