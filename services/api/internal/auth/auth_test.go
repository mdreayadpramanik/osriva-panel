package auth

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var fastParams = Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery", fastParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	ok, rehash, err := VerifyPassword("correct horse battery", h, fastParams)
	if err != nil || !ok || rehash {
		t.Fatalf("verify: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, _, err = VerifyPassword("wrong horse battery", h, fastParams)
	if err != nil || ok {
		t.Fatalf("wrong password accepted: ok=%v err=%v", ok, err)
	}
}

func TestVerifyFlagsRehash(t *testing.T) {
	h, _ := HashPassword("correct horse battery", fastParams)
	stronger := fastParams
	stronger.Iterations = 2
	ok, rehash, err := VerifyPassword("correct horse battery", h, stronger)
	if err != nil || !ok || !rehash {
		t.Fatalf("expected rehash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same password here", fastParams)
	b, _ := HashPassword("same password here", fastParams)
	if a == b {
		t.Fatal("two hashes of the same password are identical")
	}
}

func TestVerifyRejectsMalformedAndAbusiveHashes(t *testing.T) {
	for _, h := range []string{
		"", "plaintext", "$bcrypt$x", "$argon2id$v=19$m=1024,t=1,p=1$c2FsdA$",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5",
	} {
		if _, _, err := VerifyPassword("x", h, fastParams); err == nil {
			t.Errorf("hash %q: expected error", h)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := map[string]bool{
		"short":                  false,
		"            ":           false,
		"admin@example.com":      false,
		"a perfectly fine pass!": true,
	}
	for pw, valid := range cases {
		reason := ValidatePasswordPolicy(pw, "admin@example.com")
		if (reason == "") != valid {
			t.Errorf("%q: valid=%v reason=%q", pw, valid, reason)
		}
	}
}

// RFC 6238 Appendix B test vectors (SHA-1, truncated to 6 digits).
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	vectors := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	}
	for unix, want := range vectors {
		got, err := TOTPCode(secret, TOTPStep(time.Unix(unix, 0)))
		if err != nil || got != want {
			t.Errorf("t=%d: got %s want %s (err %v)", unix, got, want, err)
		}
	}
}

func TestValidateTOTPSkewAndReplay(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	step := TOTPStep(now)
	prev, _ := TOTPCode(secret, step-1)
	if got, ok := ValidateTOTP(secret, prev, now, 0); !ok || got != step-1 {
		t.Fatal("previous-step code should be accepted")
	}
	cur, _ := TOTPCode(secret, step)
	if _, ok := ValidateTOTP(secret, cur, now, step); ok {
		t.Fatal("replayed step accepted")
	}
	old, _ := TOTPCode(secret, step-5)
	if _, ok := ValidateTOTP(secret, old, now, 0); ok {
		t.Fatal("stale code accepted")
	}
	if _, ok := ValidateTOTP(secret, "12345", now, 0); ok {
		t.Fatal("short code accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := TOTPURI("OSRIVA Panel", "a@b.c", "ABC")
	if !strings.HasPrefix(uri, "otpauth://totp/OSRIVA%20Panel:a@b.c?") || !strings.Contains(uri, "secret=ABC") {
		t.Fatalf("unexpected uri %s", uri)
	}
}

func TestSessionTokenAndID(t *testing.T) {
	tok, h, err := NewSessionToken()
	if err != nil || len(tok) < 40 || len(h) != 32 {
		t.Fatalf("token=%q hash len=%d err=%v", tok, len(h), err)
	}
	if string(HashToken(tok)) != string(h) {
		t.Fatal("hash mismatch")
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if id := NewID(); !re.MatchString(id) {
		t.Fatalf("bad uuid %s", id)
	}
}
