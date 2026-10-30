package sealer

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTripAndFormat(t *testing.T) {
	k := newKey(t)
	for name, secret := range map[string]string{
		"base64": base64.StdEncoding.EncodeToString(k),
		"hex":    hex.EncodeToString(k),
	} {
		t.Run(name, func(t *testing.T) {
			s, err := New(secret)
			if err != nil {
				t.Fatal(err)
			}
			for _, plain := range [][]byte{[]byte("hello"), {}, bytes.Repeat([]byte{0xff}, 5000)} {
				sealed, err := s.Seal(plain)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(sealed, "v1.") || strings.ContainsAny(sealed, "+/=") {
					t.Fatalf("unexpected format %q", sealed)
				}
				got, err := s.Open(sealed)
				if err != nil || !bytes.Equal(got, plain) {
					t.Fatalf("Open = %q, %v", got, err)
				}
			}
		})
	}
}

func TestNonceIsRandom(t *testing.T) {
	s, _ := New(hex.EncodeToString(newKey(t)))
	a, _ := s.Seal([]byte("x"))
	b, _ := s.Seal([]byte("x"))
	if a == b {
		t.Fatal("two seals of the same value are identical")
	}
}

func TestTamperDetection(t *testing.T) {
	s, _ := New(hex.EncodeToString(newKey(t)))
	sealed, _ := s.Seal([]byte("secret token"))
	body := []byte(strings.TrimPrefix(sealed, "v1."))
	for i := range body {
		mut := append([]byte(nil), body...)
		if mut[i] == 'A' {
			mut[i] = 'B'
		} else {
			mut[i] = 'A'
		}
		if _, err := s.Open("v1." + string(mut)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("flip at %d: err = %v, want ErrInvalid", i, err)
		}
	}
	for _, bad := range []string{"", "v1.", "v2." + string(body), string(body), "v1.!!!", "v1." + string(body[:10]), sealed + "AA"} {
		if _, err := s.Open(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Open(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestRotation(t *testing.T) {
	oldKey, newK := hex.EncodeToString(newKey(t)), hex.EncodeToString(newKey(t))
	oldS, _ := New(oldKey)
	sealedOld, _ := oldS.Seal([]byte("v"))

	rotated, err := New(newK, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotated.Open(sealedOld); err != nil || string(got) != "v" {
		t.Fatalf("rotated Open(old) = %q, %v", got, err)
	}
	// New values are sealed with the first key only.
	sealedNew, _ := rotated.Seal([]byte("w"))
	if _, err := oldS.Open(sealedNew); !errors.Is(err, ErrInvalid) {
		t.Fatal("old-only sealer opened a value sealed with the new key")
	}
	newOnly, _ := New(newK)
	if got, err := newOnly.Open(sealedNew); err != nil || string(got) != "w" {
		t.Fatalf("new-only Open = %q, %v", got, err)
	}
	if _, err := newOnly.Open(sealedOld); !errors.Is(err, ErrInvalid) {
		t.Fatal("dropped key still opens old values")
	}
}

func TestBadKeys(t *testing.T) {
	for _, bad := range []string{"", "short", hex.EncodeToString(make([]byte, 16)), base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) succeeded", bad)
		}
	}
	if _, err := New(); err == nil {
		t.Error("New() with no keys succeeded")
	}
}
