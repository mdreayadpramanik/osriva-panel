// Package state persists the agent's identity: endpoint, server ID, CA,
// certificate and private key.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	fileConfig = "agent.json"
	fileCA     = "ca.crt"
	fileCert   = "agent.crt"
	fileKey    = "agent.key"
)

type Config struct {
	ServerID      string `json:"server_id"`
	ServerName    string `json:"server_name"`
	PanelURL      string `json:"panel_url"`
	AgentEndpoint string `json:"agent_endpoint"`
	CAFingerprint string `json:"ca_fingerprint"`
	// FileRoots limits the file manager (empty = tasks.DefaultRoots()). Only editable on the server itself.
	FileRoots []string `json:"file_roots,omitempty"`
}

type State struct {
	Dir     string
	Config  Config
	CAPEM   []byte
	CertPEM []byte
	KeyPEM  []byte
	// Secured reports whether Save restricted the directory to administrators
	// (always on Unix; on Windows only from an elevated process).
	Secured bool
}

// DefaultDir is the platform's system configuration location for the agent.
func DefaultDir() string {
	switch runtime.GOOS {
	case "windows":
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "OSRIVA", "agent")
		}
		return `C:\ProgramData\OSRIVA\agent`
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return "/usr/local/etc/osriva-agent"
	default:
		return "/etc/osriva-agent"
	}
}

var ErrNotEnrolled = errors.New("agent is not enrolled (run `osriva-agent enroll`)")

func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, fileConfig))
	return err == nil
}

func Load(dir string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, fileConfig))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	s := &State{Dir: dir}
	if err := json.Unmarshal(raw, &s.Config); err != nil {
		return nil, fmt.Errorf("%s: %w", fileConfig, err)
	}
	for name, dst := range map[string]*[]byte{fileCA: &s.CAPEM, fileCert: &s.CertPEM, fileKey: &s.KeyPEM} {
		if *dst, err = os.ReadFile(filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// writeAtomic writes via a temp file and rename so a crash never leaves a
// half-written credential. Mode 0600 restricts access to the owner on Unix;
// on Windows the directory ACL (set by the installer) is what protects it.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *State) Save() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	// Restrict the directory before any secret is written into it.
	secured, err := secureDir(s.Dir)
	if err != nil {
		return err
	}
	s.Secured = secured
	cfg, err := json.MarshalIndent(s.Config, "", "  ")
	if err != nil {
		return err
	}
	// Key and certificate first, config last: config marks enrolment as complete.
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{fileKey, s.KeyPEM, 0o600}, {fileCert, s.CertPEM, 0o644}, {fileCA, s.CAPEM, 0o644}, {fileConfig, append(cfg, '\n'), 0o600}} {
		if err := writeAtomic(filepath.Join(s.Dir, f.name), f.data, f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	return nil
}

// SaveCredentials replaces the certificate and key after renewal.
func (s *State) SaveCredentials(certPEM, keyPEM []byte) error {
	if err := writeAtomic(filepath.Join(s.Dir, fileKey), keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.Dir, fileCert), certPEM, 0o644); err != nil {
		return err
	}
	s.CertPEM, s.KeyPEM = certPEM, keyPEM
	return nil
}
