// Package luci implements the Deco local HTTP API (/cgi-bin/luci) transport.
// Protocol details follow CLAUDE.md §4.1; the wire format is unverified on
// real hardware until the Phase 0 spike runs.
package luci

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" //nolint:gosec // the device protocol mandates MD5 for the credential hash
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
)

// keyMaterial is the per-process AES-128 key and IV. Their ASCII bytes are the raw key and IV.
type keyMaterial struct {
	key string // 16 ASCII digits
	iv  string // 16 ASCII digits
}

// newKeyMaterial returns two random 16-digit decimal strings with no leading zero.
func newKeyMaterial() (keyMaterial, error) {
	k, err := randomDigits16()
	if err != nil {
		return keyMaterial{}, err
	}
	iv, err := randomDigits16()
	if err != nil {
		return keyMaterial{}, err
	}
	return keyMaterial{key: k, iv: iv}, nil
}

func randomDigits16() (string, error) {
	// [1e15, 1e16) has exactly 16 digits and no leading zero.
	n, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000_000_000))
	if err != nil {
		return "", fmt.Errorf("generating random digits: %w", err)
	}
	return fmt.Sprintf("%d", n.Int64()+1_000_000_000_000_000), nil
}

// pkcs7Pad pads to a whole number of AES blocks. A full block is added when
// the input is already aligned.
func pkcs7Pad(b []byte, blockSize int) []byte {
	pad := blockSize - len(b)%blockSize
	return append(append([]byte{}, b...), bytes.Repeat([]byte{byte(pad)}, pad)...)
}

var errBadPadding = errors.New("luci: invalid PKCS#7 padding")

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 || len(b)%blockSize != 0 {
		return nil, errBadPadding
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > blockSize || pad > len(b) {
		return nil, errBadPadding
	}
	for _, c := range b[len(b)-pad:] {
		if int(c) != pad {
			return nil, errBadPadding
		}
	}
	return b[:len(b)-pad], nil
}

func aesCBC(km keyMaterial) (cipher.Block, []byte, error) {
	if len(km.key) != 16 || len(km.iv) != 16 {
		return nil, nil, fmt.Errorf("luci: AES key and IV must be 16 bytes")
	}
	block, err := aes.NewCipher([]byte(km.key))
	if err != nil {
		return nil, nil, err
	}
	return block, []byte(km.iv), nil
}

// aesEncrypt returns AES-128-CBC(PKCS#7(plain)).
func aesEncrypt(km keyMaterial, plain []byte) ([]byte, error) {
	block, iv, err := aesCBC(km)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

// aesDecrypt reverses aesEncrypt.
func aesDecrypt(km keyMaterial, data []byte) ([]byte, error) {
	block, iv, err := aesCBC(km)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errBadPadding
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return pkcs7Unpad(out, aes.BlockSize)
}

// maxRSABits bounds device-supplied moduli so a hostile device cannot force
// very slow RSA operations.
const maxRSABits = 4096

// parseRSAPublicKey builds a key from hex modulus and exponent.
func parseRSAPublicKey(nHex, eHex string) (*rsa.PublicKey, error) {
	n, ok := new(big.Int).SetString(nHex, 16)
	if !ok || n.Sign() <= 0 || n.BitLen() > maxRSABits {
		return nil, errors.New("luci: invalid RSA modulus")
	}
	e, ok := new(big.Int).SetString(eHex, 16)
	if !ok || !e.IsInt64() || e.Int64() < 3 {
		return nil, errors.New("luci: invalid RSA exponent")
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// rsaEncryptHex applies raw PKCS#1 v1.5 encryption in chunks of (k-11) bytes
// and returns the concatenated lowercase hex of every block.
func rsaEncryptHex(pub *rsa.PublicKey, plain []byte) (string, error) {
	k := (pub.N.BitLen() + 7) / 8
	chunk := k - 11
	if chunk <= 0 {
		return "", errors.New("luci: RSA key too small")
	}
	var sb strings.Builder
	for off := 0; off < len(plain); off += chunk {
		end := min(off+chunk, len(plain))
		ct, err := rsa.EncryptPKCS1v15(rand.Reader, pub, plain[off:end])
		if err != nil {
			return "", fmt.Errorf("RSA encrypt: %w", err)
		}
		sb.WriteString(hex.EncodeToString(ct))
	}
	return sb.String(), nil
}

// signedBody builds the form body for a signed request:
//
//	sign=<rsa-hex(signPlain)>&data=<urlescape(base64(aes(payload)))>
//
// signPlain is always "k=..&i=..&h=..&s=.." (amosyuen's client does this on
// every call; the spike must confirm it). s = seq + len(base64 data).
func signedBody(km keyMaterial, signKey *rsa.PublicKey, seq int, h string, payload []byte) (string, error) {
	enc, err := aesEncrypt(km, payload)
	if err != nil {
		return "", err
	}
	data := base64.StdEncoding.EncodeToString(enc)
	s := seq + len(data)
	signPlain := fmt.Sprintf("k=%s&i=%s&h=%s&s=%d", km.key, km.iv, h, s)
	sign, err := rsaEncryptHex(signKey, []byte(signPlain))
	if err != nil {
		return "", err
	}
	return "sign=" + sign + "&data=" + url.QueryEscape(data), nil
}

// credentialHash is md5hex("admin" + password), the h value in the sign string.
func credentialHash(password string) string {
	sum := md5.Sum([]byte("admin" + password)) //nolint:gosec // protocol-mandated
	return hex.EncodeToString(sum[:])
}
