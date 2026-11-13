// Package seal keeps a secret (a project's Gapi token, a CRM's key) unreadable
// in the database: AES-GCM under a key derived from the service's own secret,
// so a copy of the database opens nothing.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

type Sealer struct{ aead cipher.AEAD }

// New derives the sealing key from the service's secret; the label keeps it
// apart from anything else the secret is used for.
func New(secret string) (*Sealer, error) {
	if len(secret) < 16 {
		return nil, errors.New("the service secret is at least 16 characters")
	}
	key := sha256.Sum256([]byte("gapi-voice-studio seal v1\n" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead}, nil
}

func (s *Sealer) Seal(plain string) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, []byte(plain), nil)
}

func (s *Sealer) Open(sealed []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value is too short")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errors.New("sealed under another secret")
	}
	return string(plain), nil
}
