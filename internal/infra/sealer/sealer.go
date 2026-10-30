// Package sealer implements application.Sealer with AES-256-GCM.
package sealer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const prefix = "v1."

// ErrInvalid is returned by Open for anything that is not a value this
// sealer produced with one of its keys (bad format, tampering, wrong key).
var ErrInvalid = errors.New("sealed value is invalid")

// AESGCM seals values with the first key and opens them with any key, so a
// key can be rotated by putting the new one first and keeping the old one
// until every sealed value has expired.
type AESGCM struct {
	aeads []cipher.AEAD
}

// New builds a sealer from secrets, each a 32-byte key encoded as base64 or
// hex. The first secret encrypts; all of them decrypt.
func New(secrets ...string) (*AESGCM, error) {
	if len(secrets) == 0 {
		return nil, errors.New("sealer: at least one key is required")
	}
	s := &AESGCM{}
	for i, secret := range secrets {
		key, err := decodeKey(secret)
		if err != nil {
			return nil, fmt.Errorf("sealer: key %d: %w", i, err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("sealer: key %d: %w", i, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("sealer: key %d: %w", i, err)
		}
		s.aeads = append(s.aeads, aead)
	}
	return s, nil
}

// decodeKey accepts hex (64 chars) or base64 (std or url, padded or not).
func decodeKey(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) == 64 {
		if key, err := hex.DecodeString(secret); err == nil {
			return key, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(secret); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("must be 32 bytes encoded as base64 or hex")
}

func (s *AESGCM) Seal(plain []byte) (string, error) {
	aead := s.aeads[0]
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("sealer: random nonce: %w", err)
	}
	// Seal appends the ciphertext to the nonce, so one buffer carries both.
	return prefix + base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil)), nil
}

func (s *AESGCM) Open(sealed string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return nil, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(rest) // strict: one canonical text per value
	if err != nil {
		return nil, ErrInvalid
	}
	for _, aead := range s.aeads {
		if len(raw) < aead.NonceSize()+aead.Overhead() {
			continue
		}
		nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
		if plain, err := aead.Open(nil, nonce, ct, nil); err == nil {
			return plain, nil
		}
	}
	return nil, ErrInvalid
}
