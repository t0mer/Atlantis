package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	b, err := New(NewKey())
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := b.Seal("T0p-secret")
	s2, _ := b.Seal("T0p-secret")
	if s1 == s2 || strings.Contains(s1, "T0p") || !strings.HasPrefix(s1, "v1:") {
		t.Fatalf("sealed values must be random and opaque: %s %s", s1, s2)
	}
	if pt, err := b.Open(s1); err != nil || pt != "T0p-secret" {
		t.Fatal(pt, err)
	}
	if e, _ := b.Seal(""); e != "" {
		t.Fatal("empty must stay empty")
	}
}

func TestWrongKeyAndTamperFail(t *testing.T) {
	a, _ := New(NewKey())
	b, _ := New(NewKey())
	s, _ := a.Seal("x")
	if _, err := b.Open(s); !errors.Is(err, ErrBadSecret) {
		t.Fatal("wrong key opened the secret")
	}
	if _, err := a.Open(s[:len(s)-2] + "AA"); !errors.Is(err, ErrBadSecret) {
		t.Fatal("tampered value opened")
	}
	if _, err := a.Open("plaintext"); !errors.Is(err, ErrBadSecret) {
		t.Fatal("plaintext accepted as sealed")
	}
}

func TestKeyValidation(t *testing.T) {
	for _, k := range []string{"", "not base64!", "dGVzdA=="} {
		if _, err := New(k); err == nil {
			t.Errorf("accepted key %q", k)
		}
	}
}

func TestKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), KeyFileName)
	if _, _, err := LoadKeyFile(p, false); !errors.Is(err, ErrNoKeyFile) {
		t.Fatalf("missing file without create: %v", err)
	}
	k, created, err := LoadKeyFile(p, true)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	k2, created, err := LoadKeyFile(p, true)
	if err != nil || created || k2 != k {
		t.Fatal("the existing key must be reused", err, created)
	}
	if _, err := New(k); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("not a key"), 0o600)
	if _, _, err := LoadKeyFile(p, true); err == nil {
		t.Fatal("a broken key file must not be replaced or accepted")
	}
}
