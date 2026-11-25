package tplinkcloud

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// The Deco app signs every account and device call with HMAC-SHA1 over
// "<base64(md5(body))>\n<timestamp>\n<nonce>\n<path>" and sends it in
// X-Authorization. Unlike the Kasa/Tapo apps it pins the timestamp to a fixed
// value and emits the signature as lowercase hex (docs/tmp-protocol.md §3).
const signTimestamp = "9999999999"

// AppKeys are the client credentials embedded in the Deco app. They are not
// shipped with Atlantis; the operator supplies them (see README).
type AppKeys struct {
	AccessKey string
	Secret    string
}

func (k AppKeys) valid() bool { return k.AccessKey != "" && k.Secret != "" }

// contentMD5 is base64(md5(body)).
func contentMD5(body []byte) string {
	sum := md5.Sum(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// signature returns the X-Authorization header value for a request with the
// given body and URL path (path only: no host, no query).
func (k AppKeys) signature(body []byte, nonce, path string) string {
	msg := signTimestamp + "\n" + nonce + "\n" + path
	if len(body) > 0 {
		msg = contentMD5(body) + "\n" + msg
	}
	mac := hmac.New(sha1.New, []byte(k.Secret))
	mac.Write([]byte(msg))
	return fmt.Sprintf("Timestamp=%s, Nonce=%s, AccessKey=%s, Signature=%s",
		signTimestamp, nonce, k.AccessKey, hex.EncodeToString(mac.Sum(nil)))
}
