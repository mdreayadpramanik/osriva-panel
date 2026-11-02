// Package pki implements the OSRIVA internal certificate authority used for
// agent mutual TLS (see docs/adr/0004-agent-enrolment-and-mtls.md).
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

const (
	CAValidity         = 10 * 365 * 24 * time.Hour
	ClientCertValidity = 30 * 24 * time.Hour
	serverCertValidity = 90 * 24 * time.Hour
	clockSkew          = 5 * time.Minute
)

var ErrInvalidCSR = errors.New("pki: invalid certificate signing request")

type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     crypto.Signer
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// NewCA creates a self-signed ECDSA P-256 CA.
func NewCA(now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "OSRIVA Panel Agent CA", Organization: []string{"OSRIVA Panel"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return &CA{Cert: cert, CertPEM: pemEncode("CERTIFICATE", der), key: key}, nil
}

// MarshalKey returns the CA private key as PKCS#8 DER (encrypt before storing).
func (ca *CA) MarshalKey() ([]byte, error) { return x509.MarshalPKCS8PrivateKey(ca.key) }

func LoadCA(certPEM, keyDER []byte) (*CA, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("pki: bad CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, err
	}
	signer, ok := k.(crypto.Signer)
	if !ok || !publicKeysEqual(signer.Public(), cert.PublicKey) {
		return nil, errors.New("pki: CA key does not match certificate")
	}
	return &CA{Cert: cert, CertPEM: certPEM, key: signer}, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	ea, ok := a.(eq)
	return ok && ea.Equal(b)
}

// Fingerprint is the SHA-256 of the certificate DER, as "sha256:<hex>".
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert) }

func acceptableKey(pub crypto.PublicKey) bool {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return k.Curve == elliptic.P256() || k.Curve == elliptic.P384()
	case ed25519.PublicKey:
		return true
	case *rsa.PublicKey:
		return k.N.BitLen() >= 3072
	}
	return false
}

// SignClientCSR issues a client certificate for commonName (the server ID).
// Only the CSR's public key is used; its subject and extensions are ignored.
func (ca *CA) SignClientCSR(csrPEM []byte, commonName string, now time.Time) (*x509.Certificate, []byte, error) {
	b, _ := pem.Decode(csrPEM)
	if b == nil || b.Type != "CERTIFICATE REQUEST" {
		return nil, nil, ErrInvalidCSR
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, nil, ErrInvalidCSR
	}
	if !acceptableKey(csr.PublicKey) {
		return nil, nil, fmt.Errorf("%w: key must be ECDSA P-256/P-384, Ed25519 or RSA >= 3072", ErrInvalidCSR)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"OSRIVA Panel Agent"}},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(ClientCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, pemEncode("CERTIFICATE", der), nil
}

// ServerCertificate issues a TLS certificate for the agent listener.
func (ca *CA) ServerCertificate(hosts []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "OSRIVA Panel agent endpoint"},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(serverCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// Pool returns a cert pool containing only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

func pemEncode(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// SerialHex is the canonical string form of a certificate serial.
func SerialHex(cert *x509.Certificate) string { return cert.SerialNumber.Text(16) }
