// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ManifestFile is the name of the manifest inside a plugin directory.
const ManifestFile = "plugin.json"

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	platformRe = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)
	exeRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hexRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Executable is the binary for one platform, relative to the plugin directory.
type Executable struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Signature struct {
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"` // base64 Ed25519 signature over Canonical()
}

// Manifest is plugin.json. Executables is keyed by "GOOS/GOARCH".
type Manifest struct {
	Name        string                `json:"name"`
	Version     string                `json:"version"`
	Publisher   string                `json:"publisher"`
	Description string                `json:"description"`
	Protocol    int                   `json:"protocol"`
	Kinds       []string              `json:"kinds"`
	Executables map[string]Executable `json:"executables"`
	Signature   *Signature            `json:"signature,omitempty"`
}

// Canonical is the signed form: the manifest without its signature, as Go's
// encoding/json writes it (fields in declaration order, map keys sorted).
func (m Manifest) Canonical() []byte {
	m.Signature = nil
	b, _ := json.Marshal(m)
	return b
}

// Validate checks the manifest's fields (not the signature).
func (m *Manifest) Validate() error {
	switch {
	case !nameRe.MatchString(m.Name):
		return errors.New("name must be 1-64 lowercase letters, digits or dashes")
	case m.Version == "" || len(m.Version) > 64 || strings.ContainsAny(m.Version, " \r\n"):
		return errors.New("version is required")
	case len(m.Publisher) > 200 || len(m.Description) > 1000:
		return errors.New("publisher or description too long")
	case m.Protocol != Protocol:
		return fmt.Errorf("protocol must be %d", Protocol)
	case len(m.Kinds) == 0:
		return errors.New("at least one kind is required")
	case len(m.Executables) == 0:
		return errors.New("at least one executable is required")
	}
	for _, k := range m.Kinds {
		if k != KindNotifier {
			return fmt.Errorf("unknown kind %q", k)
		}
	}
	for p, e := range m.Executables {
		if !platformRe.MatchString(p) || !exeRe.MatchString(e.Path) || !hexRe.MatchString(e.SHA256) {
			return fmt.Errorf("invalid executable entry for %q", p)
		}
	}
	return nil
}

// KeyID identifies a public key: the first 16 bytes of its SHA-256, in hex.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("a public key must be 32 bytes in standard base64")
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey decodes a base64 Ed25519 private key (64 bytes).
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("a private key must be 64 bytes in standard base64")
	}
	return ed25519.PrivateKey(b), nil
}

// Sign sets the manifest signature.
func (m *Manifest) Sign(key ed25519.PrivateKey) {
	pub := key.Public().(ed25519.PublicKey)
	m.Signature = &Signature{KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(key, m.Canonical()))}
}

// VerifySignature checks the signature against the trusted keys and returns the signing key id.
func (m *Manifest) VerifySignature(trusted []ed25519.PublicKey) (string, error) {
	if m.Signature == nil {
		return "", errors.New("the manifest is not signed")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature.Sig)
	if err != nil {
		return "", errors.New("the signature is not base64")
	}
	for _, k := range trusted {
		if KeyID(k) == m.Signature.KeyID {
			if !ed25519.Verify(k, m.Canonical(), sig) {
				return "", errors.New("the signature does not verify")
			}
			return m.Signature.KeyID, nil
		}
	}
	return "", fmt.Errorf("the signing key %s is not trusted", m.Signature.KeyID)
}

// HashFile returns the hex SHA-256 of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LoadManifest reads dir/plugin.json.
func LoadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	return &m, nil
}
