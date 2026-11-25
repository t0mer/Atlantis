package luci

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func testKeyMaterial() keyMaterial {
	return keyMaterial{key: "1234567890123456", iv: "6543210987654321"}
}

func TestRandomDigits16(t *testing.T) {
	for i := 0; i < 200; i++ {
		s, err := randomDigits16()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 16 || s[0] == '0' {
			t.Fatalf("bad key string %q", s)
		}
		if _, err := strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatalf("not decimal: %q", s)
		}
	}
}

func TestPKCS7PadExactMultipleAddsFullBlock(t *testing.T) {
	in := bytes.Repeat([]byte{'a'}, 16)
	out := pkcs7Pad(in, 16)
	if len(out) != 32 {
		t.Fatalf("len = %d, want 32", len(out))
	}
	back, err := pkcs7Unpad(out, 16)
	if err != nil || !bytes.Equal(back, in) {
		t.Fatalf("unpad mismatch: %v", err)
	}
}

func TestPKCS7UnpadRejectsBadPadding(t *testing.T) {
	bad := append(bytes.Repeat([]byte{'a'}, 14), 0x02, 0x03)
	if _, err := pkcs7Unpad(bad, 16); err == nil {
		t.Fatal("expected error for inconsistent padding")
	}
	if _, err := pkcs7Unpad(bytes.Repeat([]byte{0}, 16), 16); err == nil {
		t.Fatal("expected error for zero padding byte")
	}
}

func TestAESRoundTrip(t *testing.T) {
	km := testKeyMaterial()
	for _, msg := range []string{"", "x", strings.Repeat("y", 16), "שלום עולם — Hebrew round trip"} {
		enc, err := aesEncrypt(km, []byte(msg))
		if err != nil {
			t.Fatal(err)
		}
		if len(enc)%16 != 0 {
			t.Fatalf("ciphertext not block aligned: %d", len(enc))
		}
		dec, err := aesDecrypt(km, enc)
		if err != nil {
			t.Fatal(err)
		}
		if string(dec) != msg {
			t.Fatalf("round trip %q != %q", dec, msg)
		}
	}
}

func TestRSAEncryptChunksLongPlaintext(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(strings.Repeat("k=1234&i=5678&h=abc&s=99999&", 10)) // > 117 bytes, needs 3+ blocks
	out, err := rsaEncryptHex(&priv.PublicKey, plain)
	if err != nil {
		t.Fatal(err)
	}
	const blockHex = 256 // 128-byte block for a 1024-bit key
	if len(out)%blockHex != 0 || len(out) < 3*blockHex {
		t.Fatalf("unexpected output length %d", len(out))
	}
	var got []byte
	for i := 0; i < len(out); i += blockHex {
		raw, err := hex.DecodeString(out[i : i+blockHex])
		if err != nil {
			t.Fatal(err)
		}
		pt, err := rsa.DecryptPKCS1v15(nil, priv, raw)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, pt...)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("chunked RSA round trip mismatch")
	}
}

func TestSignedBodyFormatAndSignature(t *testing.T) {
	km := testKeyMaterial()
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	payload := []byte(`{"operation":"read"}`)
	const seq = 4242

	body, err := signedBody(km, &priv.PublicKey, seq, "deadbeef", payload)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := url.ParseQuery(body)
	if err != nil {
		t.Fatal(err)
	}
	data := vals.Get("data")
	if data == "" || vals.Get("sign") == "" {
		t.Fatalf("missing fields in %q", body)
	}
	// data must be the base64 of AES(payload), URL-escaped on the wire.
	enc, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := aesDecrypt(km, enc)
	if err != nil || !bytes.Equal(dec, payload) {
		t.Fatalf("payload not recoverable: %v", err)
	}
	// sign decrypts to the k/i/h/s string with s = seq + len(base64 data).
	sign := vals.Get("sign")
	var plain []byte
	for i := 0; i < len(sign); i += 256 {
		raw, _ := hex.DecodeString(sign[i : i+256])
		pt, err := rsa.DecryptPKCS1v15(nil, priv, raw)
		if err != nil {
			t.Fatal(err)
		}
		plain = append(plain, pt...)
	}
	want := "k=1234567890123456&i=6543210987654321&h=deadbeef&s=" + strconv.Itoa(seq+len(data))
	if string(plain) != want {
		t.Fatalf("sign plain = %q, want %q", plain, want)
	}
}

func TestSignedBodyEscapesBase64Specials(t *testing.T) {
	km := testKeyMaterial()
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	// Find a payload whose base64 contains '+' or '/' so escaping is exercised.
	var payload []byte
	for i := 0; ; i++ {
		p := []byte(strconv.Itoa(i) + `{"params":{"password":"x"},"operation":"login"}`)
		enc, _ := aesEncrypt(km, p)
		b := base64.StdEncoding.EncodeToString(enc)
		if strings.ContainsAny(b, "+/") {
			payload = p
			break
		}
	}
	body, err := signedBody(km, &priv.PublicKey, 1, "h", payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := body[strings.Index(body, "&data=")+len("&data="):]
	if strings.ContainsAny(raw, "+/") {
		t.Fatalf("data not URL-escaped: %s", raw)
	}
}

func TestCredentialHash(t *testing.T) {
	// Vector computed independently: printf 'adminpw' | md5sum
	const want = "70e76a15da00e6301ade718cc9416f79"
	if got := credentialHash("pw"); got != want {
		t.Fatalf("credentialHash = %q, want %q", got, want)
	}
}

// Some Deco firmware uses 512-bit RSA keys, which crypto/rsa refuses.
// Encrypt with one and decrypt by hand to check the PKCS#1 v1.5 layout.
func TestPKCS1v15With512BitKey(t *testing.T) {
	var p, q *big.Int
	for {
		p, _ = rand.Prime(rand.Reader, 256)
		q, _ = rand.Prime(rand.Reader, 256)
		if n := new(big.Int).Mul(p, q); n.BitLen() == 512 {
			break
		}
	}
	n := new(big.Int).Mul(p, q)
	phi := new(big.Int).Mul(new(big.Int).Sub(p, big.NewInt(1)), new(big.Int).Sub(q, big.NewInt(1)))
	e := big.NewInt(65537)
	d := new(big.Int).ModInverse(e, phi)
	if d == nil {
		t.Skip("e not invertible for these primes")
	}
	pub := &rsa.PublicKey{N: n, E: 65537}
	msg := []byte("k=1234567890123456&i=1234567890123456")
	ct, err := rsaEncryptHex(pub, msg)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for i := 0; i < len(ct); i += 128 { // 64-byte blocks in hex
		c, _ := new(big.Int).SetString(ct[i:i+128], 16)
		em := new(big.Int).Exp(c, d, n).FillBytes(make([]byte, 64))
		if em[0] != 0 || em[1] != 2 {
			t.Fatalf("bad header %x", em[:2])
		}
		sep := bytes.IndexByte(em[2:], 0)
		if sep < 8 {
			t.Fatalf("padding too short: %d", sep)
		}
		got = append(got, em[2+sep+1:]...)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round trip: %q", got)
	}
}

// The hand-rolled padding must stay compatible with crypto/rsa.
func TestPKCS1v15InteropWithStdlib(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := pkcs1v15Encrypt(&priv.PublicKey, []byte("hello deco"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := rsa.DecryptPKCS1v15(nil, priv, ct)
	if err != nil || string(pt) != "hello deco" {
		t.Fatal(pt, err)
	}
}
