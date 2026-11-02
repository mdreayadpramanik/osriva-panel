// Package agentclient implements the agent side of the OSRIVA agent protocol:
// key generation, enrolment with CA pinning, reports and certificate renewal.
package agentclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
)

// APIError is an error response from the control plane.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("control plane: %s (%s, HTTP %d)", e.Message, e.Code, e.Status)
}

// NewKey generates an ECDSA P-256 private key and returns it with a CSR.
func NewKey() (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "osriva-agent"}}, key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), nil
}

// Fingerprint returns "sha256:<hex>" of the first certificate in certPEM.
func Fingerprint(certPEM []byte) (string, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return "", errors.New("not a PEM certificate")
	}
	sum := sha256.Sum256(b.Bytes)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidatePanelURL requires https, except for loopback hosts during development.
func ValidatePanelURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid panel URL %q", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("panel URL must use https:// (plain http is only allowed for localhost)")
	}
	return u, nil
}

func postJSON(ctx context.Context, hc *http.Client, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "osriva-agent")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e agentproto.Error
		_ = json.Unmarshal(raw, &e)
		if e.Error.Code == "" {
			e.Error.Code, e.Error.Message = "http_error", strings.TrimSpace(string(raw))
		}
		return &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	return json.Unmarshal(raw, out)
}

// EnrollResult holds everything the agent must persist after enrolment.
type EnrollResult struct {
	Response agentproto.EnrollResponse
	KeyPEM   []byte
}

// Enroll exchanges a one-time token for a client certificate. The panel's CA
// is fetched and checked against caFingerprint (from the enrolment command)
// BEFORE the token is sent, so a spoofed panel never sees the token and cannot
// impose a different CA. panelRoots is nil for the system roots.
func Enroll(ctx context.Context, panelURL, token, caFingerprint, agentVersion string, panelRoots *x509.CertPool) (*EnrollResult, error) {
	u, err := ValidatePanelURL(panelURL)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(caFingerprint, "sha256:") || len(caFingerprint) != len("sha256:")+64 {
		return nil, errors.New("CA fingerprint must look like sha256:<64 hex characters>")
	}
	keyPEM, csrPEM, err := NewKey()
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: panelRoots},
	}}
	if err := verifyPanelCA(ctx, hc, u.String(), caFingerprint); err != nil {
		return nil, err
	}
	var resp agentproto.EnrollResponse
	err = postJSON(ctx, hc, u.String()+agentproto.EnrollPath,
		agentproto.EnrollRequest{Token: token, CSRPEM: string(csrPEM), AgentVersion: agentVersion}, &resp)
	if err != nil {
		return nil, err
	}
	got, err := Fingerprint([]byte(resp.CAPEM))
	if err != nil {
		return nil, fmt.Errorf("control plane returned an invalid CA: %w", err)
	}
	if !strings.EqualFold(got, caFingerprint) {
		return nil, fmt.Errorf("CA fingerprint mismatch: expected %s, panel returned %s. Refusing to enrol", caFingerprint, got)
	}
	if _, err := tls.X509KeyPair([]byte(resp.CertificatePEM), keyPEM); err != nil {
		return nil, fmt.Errorf("issued certificate does not match the generated key: %w", err)
	}
	if !strings.HasPrefix(resp.AgentEndpoint, "https://") {
		return nil, fmt.Errorf("control plane returned a non-https agent endpoint %q", resp.AgentEndpoint)
	}
	return &EnrollResult{Response: resp, KeyPEM: keyPEM}, nil
}

// Client talks to the mutual-TLS agent listener.
type Client struct {
	endpoint string
	hc       *http.Client
}

// New builds a client that presents certPEM/keyPEM and trusts only caPEM.
func New(endpoint string, caPEM, certPEM, keyPEM []byte) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("agent endpoint must be an https:// URL")
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load agent certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid CA certificate")
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{pair}, ServerName: u.Hostname(),
		},
		ForceAttemptHTTP2: true,
	}
	return &Client{endpoint: u.String(), hc: &http.Client{Timeout: 30 * time.Second, Transport: tr}}, nil
}

func (c *Client) Report(ctx context.Context, req agentproto.ReportRequest) (*agentproto.ReportResponse, error) {
	var resp agentproto.ReportResponse
	if err := postJSON(ctx, c.hc, c.endpoint+agentproto.ReportPath, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Renew requests a fresh certificate for a newly generated key.
func (c *Client) Renew(ctx context.Context) (certPEM, keyPEM []byte, err error) {
	keyPEM, csrPEM, err := NewKey()
	if err != nil {
		return nil, nil, err
	}
	var resp agentproto.RenewResponse
	if err := postJSON(ctx, c.hc, c.endpoint+agentproto.RenewPath, agentproto.RenewRequest{CSRPEM: string(csrPEM)}, &resp); err != nil {
		return nil, nil, err
	}
	if _, err := tls.X509KeyPair([]byte(resp.CertificatePEM), keyPEM); err != nil {
		return nil, nil, fmt.Errorf("renewed certificate does not match key: %w", err)
	}
	return []byte(resp.CertificatePEM), keyPEM, nil
}

// NeedsRenewal reports whether less than a third of the certificate lifetime remains.
func NeedsRenewal(certPEM []byte, now time.Time) (bool, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return false, errors.New("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return false, err
	}
	life := cert.NotAfter.Sub(cert.NotBefore)
	return cert.NotAfter.Sub(now) < life/3, nil
}

func verifyPanelCA(ctx context.Context, hc *http.Client, panelURL, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, panelURL+agentproto.CAPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "osriva-agent")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("reach panel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("panel CA endpoint returned HTTP %d", resp.StatusCode)
	}
	var ca agentproto.CAResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ca); err != nil {
		return fmt.Errorf("decode panel CA: %w", err)
	}
	// Compute the fingerprint ourselves; never trust the one the server states.
	got, err := Fingerprint([]byte(ca.CAPEM))
	if err != nil {
		return fmt.Errorf("panel returned an invalid CA: %w", err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("CA fingerprint mismatch: expected %s, panel has %s. Refusing to send the enrolment token", want, got)
	}
	return nil
}
