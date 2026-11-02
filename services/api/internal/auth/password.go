// Package auth contains credential primitives: password hashing, TOTP and
// session tokens. It has no knowledge of storage or HTTP.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the Argon2id cost parameters.
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultArgon2Params follows the OWASP Password Storage Cheat Sheet guidance.
var DefaultArgon2Params = Argon2Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLen: 16, KeyLen: 32}

const (
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

var ErrMalformedHash = errors.New("auth: malformed password hash")

// HashPassword returns an Argon2id hash in PHC string format.
func HashPassword(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against an encoded hash. needsRehash reports
// whether the hash was produced with parameters different from want.
func VerifyPassword(password, encoded string, want Argon2Params) (ok, needsRehash bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, ErrMalformedHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return false, false, ErrMalformedHash
	}
	// Bound the cost so a corrupted row cannot be used to exhaust memory.
	if p.Memory == 0 || p.Memory > 1024*1024 || p.Iterations == 0 || p.Iterations > 64 || p.Parallelism == 0 {
		return false, false, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, false, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return false, false, ErrMalformedHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(key)))
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false, nil
	}
	needsRehash = p.Memory != want.Memory || p.Iterations != want.Iterations ||
		p.Parallelism != want.Parallelism || uint32(len(key)) != want.KeyLen || uint32(len(salt)) != want.SaltLen
	return true, needsRehash, nil
}

// ValidatePasswordPolicy returns a human-readable reason when password is unacceptable.
func ValidatePasswordPolicy(password, email string) string {
	n := len([]rune(password))
	switch {
	case n < MinPasswordLen:
		return fmt.Sprintf("must be at least %d characters", MinPasswordLen)
	case len(password) > MaxPasswordLen:
		return fmt.Sprintf("must be at most %d bytes", MaxPasswordLen)
	case strings.TrimSpace(password) == "":
		return "must not be blank"
	case email != "" && strings.EqualFold(password, email):
		return "must not be the same as the email address"
	}
	return ""
}
