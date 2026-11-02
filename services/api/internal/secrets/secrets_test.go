package secrets

import (
	"bytes"
	"testing"
)

func testKeyring(t *testing.T) *Keyring {
	t.Helper()
	k, err := NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNewKeyringRejectsShortKey(t *testing.T) {
	if _, err := NewKeyring(make([]byte, 16)); err == nil {
		t.Fatal("expected error for 16-byte master key")
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	k := testKeyring(t)
	ct, err := k.Encrypt([]byte("top secret"), []byte("user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("top secret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	pt, err := k.Decrypt(ct, []byte("user-1"))
	if err != nil || string(pt) != "top secret" {
		t.Fatalf("round trip failed: %q %v", pt, err)
	}
}

func TestDecryptRejectsWrongAADAndTampering(t *testing.T) {
	k := testKeyring(t)
	ct, _ := k.Encrypt([]byte("x"), []byte("user-1"))
	if _, err := k.Decrypt(ct, []byte("user-2")); err == nil {
		t.Fatal("decrypt with wrong AAD should fail")
	}
	ct[len(ct)-1] ^= 1
	if _, err := k.Decrypt(ct, []byte("user-1")); err == nil {
		t.Fatal("decrypt of tampered ciphertext should fail")
	}
	if _, err := k.Decrypt([]byte{1, 2}, nil); err == nil {
		t.Fatal("decrypt of short blob should fail")
	}
}

func TestDifferentMasterKeyCannotDecrypt(t *testing.T) {
	k1 := testKeyring(t)
	k2, _ := NewKeyring(bytes.Repeat([]byte{8}, 32))
	ct, _ := k1.Encrypt([]byte("x"), nil)
	if _, err := k2.Decrypt(ct, nil); err == nil {
		t.Fatal("other key decrypted ciphertext")
	}
}

func TestCSRF(t *testing.T) {
	k := testKeyring(t)
	h := []byte("session-hash")
	tok := k.CSRFToken(h)
	if !k.VerifyCSRF(h, tok) {
		t.Fatal("valid token rejected")
	}
	if k.VerifyCSRF([]byte("other"), tok) || k.VerifyCSRF(h, "") || k.VerifyCSRF(h, tok+"x") {
		t.Fatal("invalid token accepted")
	}
}
