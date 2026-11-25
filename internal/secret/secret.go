// Package secret encrypts stored credentials (the Deco password, channel
// URLs and tokens) with AES-256-GCM under ATLANTIS_SECRET_KEY, or under a
// key file created next to the database on first start.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// prefix marks a sealed value so plaintext is never mistaken for one.
const prefix = "v1:"

// Box seals and opens secrets. It is safe for concurrent use.
type Box struct{ aead cipher.AEAD }

// NewKey returns a random key, base64-encoded, for ATLANTIS_SECRET_KEY.
func NewKey() string {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// New parses a base64 32-byte key.
func New(key string) (*Box, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return nil, errors.New("secret: ATLANTIS_SECRET_KEY is not valid base64")
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("secret: ATLANTIS_SECRET_KEY must be 32 bytes, got %d", len(k))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. The empty string stays empty.
func (b *Box) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

// ErrBadSecret means a stored value cannot be decrypted (wrong key or
// tampered data).
var ErrBadSecret = errors.New("secret: cannot decrypt (wrong ATLANTIS_SECRET_KEY?)")

// Open decrypts a value produced by Seal. The empty string stays empty.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return "", ErrBadSecret
	}
	ct, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(ct) < b.aead.NonceSize() {
		return "", ErrBadSecret
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, ct[:n], ct[n:], nil)
	if err != nil {
		return "", ErrBadSecret
	}
	return string(pt), nil
}

// KeyFileName is the key file kept next to the database when
// ATLANTIS_SECRET_KEY is not set.
const KeyFileName = "secret.key"

// ErrNoKeyFile means the key file is missing but may not be created
// (the database already holds data sealed with some other key).
var ErrNoKeyFile = errors.New("secret: the key file is missing")

// LoadKeyFile reads the key at path. If the file does not exist and create
// is true, it writes a new random key there (owner-only, created
// exclusively so two starts never race) and reports created.
func LoadKeyFile(path string, create bool) (key string, created bool, err error) {
	b, err := os.ReadFile(path)
	if err == nil {
		key = strings.TrimSpace(string(b))
		if _, err := New(key); err != nil {
			return "", false, fmt.Errorf("%s: %w", path, err)
		}
		return key, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	if !create {
		return "", false, fmt.Errorf("%w: %s", ErrNoKeyFile, path)
	}
	key = NewKey()
	// Write a temporary file, flush it, then link it into place: the link
	// fails if another start got there first (its key is then used), and a
	// reader never sees a half-written key. The directory is flushed too,
	// so the key survives a power cut as durably as the database does.
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".secret.key.*")
	if err != nil {
		return "", false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", false, err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return "", false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", false, err
	}
	if err := f.Close(); err != nil {
		return "", false, err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) { // another start won: use its key
			return LoadKeyFile(path, false)
		}
		// No hard links here (some network shares, FUSE, FAT): create the
		// file exclusively instead.
		if err := writeExclusive(path, key); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return LoadKeyFile(path, false)
			}
			return "", false, err
		}
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return key, true, nil
}

// writeExclusive creates path (failing if it exists) with the key, flushed.
func writeExclusive(path, key string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(key + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
