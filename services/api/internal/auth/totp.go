package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP per RFC 6238 with the parameters every common authenticator app supports.
const (
	TOTPPeriod = 30
	TOTPDigits = 6
	totpSkew   = 1 // accept one step before/after to tolerate clock drift
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new random 160-bit secret, base32-encoded.
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return b32.EncodeToString(raw), nil
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimRight(secret, "="), " ", ""))
	return b32.DecodeString(s)
}

// TOTPCode computes the code for a time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", fmt.Errorf("auth: invalid TOTP secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range TOTPDigits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", TOTPDigits, bin%mod), nil
}

// TOTPStep returns the time step containing t.
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// ValidateTOTP checks code against the steps around now. Steps <= lastStep are
// rejected so a code can never be used twice. On success the matched step is returned.
func ValidateTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// TOTPURI builds an otpauth:// URI for enrolment in authenticator apps.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(TOTPPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
