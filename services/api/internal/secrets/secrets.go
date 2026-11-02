// Package secrets provides authenticated encryption for secrets stored at rest
// and derives purpose-specific keys from the single OSRIVA master key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
)

// MinMasterKeyLen is the minimum accepted master key length in bytes.
const MinMasterKeyLen = 32

// ciphertextVersion prefixes every blob so the format can evolve (e.g. key rotation).
const ciphertextVersion byte = 1

var ErrInvalidCiphertext = errors.New("secrets: invalid ciphertext")

// DeriveKey derives a 32-byte subkey for purpose from the master key.
// Distinct purposes yield independent keys, so a CSRF key can never decrypt secrets.
func DeriveKey(master []byte, purpose string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("osriva/v1/" + purpose))
	return m.Sum(nil)
}

// Keyring holds the keys derived from the master key.
type Keyring struct {
	aead    cipher.AEAD
	csrfKey []byte
}

func NewKeyring(master []byte) (*Keyring, error) {
	if len(master) < MinMasterKeyLen {
		return nil, fmt.Errorf("secrets: master key must be at least %d bytes, got %d", MinMasterKeyLen, len(master))
	}
	block, err := aes.NewCipher(DeriveKey(master, "encryption"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keyring{aead: aead, csrfKey: DeriveKey(master, "csrf")}, nil
}

// Encrypt seals plaintext with AES-256-GCM. aad binds the ciphertext to its
// context (e.g. the owning row's ID) so blobs cannot be swapped between rows.
func (k *Keyring) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+k.aead.Overhead())
	out = append(out, ciphertextVersion)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, plaintext, aad), nil
}

func (k *Keyring) Decrypt(blob, aad []byte) ([]byte, error) {
	ns := k.aead.NonceSize()
	if len(blob) < 1+ns+k.aead.Overhead() || blob[0] != ciphertextVersion {
		return nil, ErrInvalidCiphertext
	}
	pt, err := k.aead.Open(nil, blob[1:1+ns], blob[1+ns:], aad)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return pt, nil
}

// CSRFToken returns the CSRF token bound to a session (by its token hash).
func (k *Keyring) CSRFToken(sessionTokenHash []byte) string {
	m := hmac.New(sha256.New, k.csrfKey)
	m.Write(sessionTokenHash)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (k *Keyring) VerifyCSRF(sessionTokenHash []byte, token string) bool {
	if token == "" {
		return false
	}
	want := k.CSRFToken(sessionTokenHash)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
