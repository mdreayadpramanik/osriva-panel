package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func csrFor(t *testing.T, key any, cn string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestCARoundTripAndClientCert(t *testing.T) {
	now := time.Now()
	ca, err := NewCA(now)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := ca.MarshalKey()
	loaded, err := LoadCA(ca.CertPEM, keyDER)
	if err != nil || loaded.Fingerprint() != ca.Fingerprint() || !strings.HasPrefix(ca.Fingerprint(), "sha256:") {
		t.Fatalf("load: %v", err)
	}

	agentKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// The CSR asks for CN "admin"; the CA must ignore it and use the server ID.
	cert, certPEM, err := loaded.SignClientCSR(csrFor(t, agentKey, "admin"), "server-123", now)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "server-123" || len(certPEM) == 0 {
		t.Fatalf("subject %q", cert.Subject.CommonName)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err != nil {
		t.Fatalf("client cert does not chain: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), CurrentTime: now.Add(31 * 24 * time.Hour), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("client cert valid after 31 days")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now}); err == nil {
		t.Fatal("client cert usable as server cert")
	}
}

func TestRejectsBadCSRs(t *testing.T) {
	ca, _ := NewCA(time.Now())
	if _, _, err := ca.SignClientCSR([]byte("not pem"), "x", time.Now()); err == nil {
		t.Fatal("garbage accepted")
	}
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	if _, _, err := ca.SignClientCSR(csrFor(t, weak, "x"), "x", time.Now()); err == nil {
		t.Fatal("1024-bit RSA accepted")
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	good := csrFor(t, k, "x")
	b, _ := pem.Decode(good)
	b.Bytes[len(b.Bytes)-1] ^= 0xff // break the signature
	if _, _, err := ca.SignClientCSR(pem.EncodeToMemory(b), "x", time.Now()); err == nil {
		t.Fatal("CSR with invalid signature accepted")
	}
}

func TestLoadCARejectsMismatchedKey(t *testing.T) {
	a, _ := NewCA(time.Now())
	b, _ := NewCA(time.Now())
	keyB, _ := b.MarshalKey()
	if _, err := LoadCA(a.CertPEM, keyB); err == nil {
		t.Fatal("mismatched key accepted")
	}
}

func TestServerCertificate(t *testing.T) {
	now := time.Now()
	ca, _ := NewCA(now)
	tc, err := ca.ServerCertificate([]string{"panel.example.com", "127.0.0.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"panel.example.com", "127.0.0.1"} {
		if _, err := tc.Leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), DNSName: host, CurrentTime: now}); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	if _, err := tc.Leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), DNSName: "evil.example.com", CurrentTime: now}); err == nil {
		t.Error("server cert valid for an unlisted host")
	}
}
