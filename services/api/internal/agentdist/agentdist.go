// Package agentdist serves agent binaries, their checksums and the one-line
// install scripts from the control plane, so enrolling a server never depends
// on an external download site.
package agentdist

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

//go:embed install.sh.tmpl
var installSh string

//go:embed install.ps1.tmpl
var installPs1 string

var fileName = regexp.MustCompile(`^osriva-agent-(linux|windows|freebsd|openbsd|netbsd)-(amd64|arm64|arm)(\.exe)?$`)

// Build is one downloadable agent binary.
type Build struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	path   string
}

// Dist is the set of agent builds found in a directory at startup.
type Dist struct {
	builds   map[string]Build // "os/arch"
	panelURL string
	sh, ps1  []byte
}

// Load scans dir for files named osriva-agent-<os>-<arch>[.exe] and hashes
// them once. Unknown files are ignored, so a request can only ever reach a
// file found here (no path traversal). dir == "" yields an empty Dist.
func Load(dir, panelURL string) (*Dist, error) {
	d := &Dist{builds: map[string]Build{}, panelURL: strings.TrimRight(panelURL, "/")}
	if err := d.renderScripts(); err != nil {
		return nil, err
	}
	if dir == "" {
		return d, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("agent dist dir: %w", err)
	}
	for _, e := range entries {
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() || (m[1] == "windows") != (m[3] == ".exe") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		sum, size, err := hashFile(p)
		if err != nil {
			return nil, err
		}
		d.builds[m[1]+"/"+m[2]] = Build{OS: m[1], Arch: m[2], Size: size, SHA256: sum, path: p}
	}
	return d, nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (d *Dist) renderScripts() error {
	data := map[string]string{"PanelURL": d.panelURL, "PanelURLQuoted": shQuote(d.panelURL), "PanelURLPSQuoted": psQuote(d.panelURL)}
	render := func(src string) ([]byte, error) {
		t, err := template.New("").Parse(src)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, data); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	var err error
	if d.sh, err = render(installSh); err != nil {
		return err
	}
	// PowerShell 5.1 reads scripts best with CRLF line endings.
	ps, err := render(installPs1)
	d.ps1 = bytes.ReplaceAll(bytes.ReplaceAll(ps, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	return err
}

func (d *Dist) Builds() []Build {
	out := make([]Build, 0, len(d.builds))
	for _, b := range d.builds {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OS+out[i].Arch < out[j].OS+out[j].Arch })
	return out
}

// Get returns the build for os/arch.
func (d *Dist) Get(goos, arch string) (Build, bool) {
	b, ok := d.builds[goos+"/"+arch]
	return b, ok
}

func (b Build) Open() (*os.File, error) { return os.Open(b.path) }

func (b Build) FileName() string {
	n := "osriva-agent-" + b.OS + "-" + b.Arch
	if b.OS == "windows" {
		n += ".exe"
	}
	return n
}

// HasFamily reports whether any build exists for the OS family ("windows" or "unix").
func (d *Dist) HasFamily(family string) bool {
	for _, b := range d.builds {
		if (b.OS == "windows") == (family == "windows") {
			return true
		}
	}
	return false
}

func (d *Dist) InstallSh() []byte  { return d.sh }
func (d *Dist) InstallPs1() []byte { return d.ps1 }

// UnixCommand and WindowsCommand are the one-liners shown with an enrolment.
// The token travels in an environment variable on Unix so it is not left in
// shell history next to the script URL.
func (d *Dist) UnixCommand(token, fingerprint string) string {
	return fmt.Sprintf("curl -fsSL %s/api/v1/agent/install.sh | sudo OSRIVA_ENROLL_TOKEN=%s sh -s -- --ca-fingerprint %s",
		d.panelURL, shQuote(token), fingerprint)
}

func (d *Dist) WindowsCommand(token, fingerprint string) string {
	return fmt.Sprintf("& ([scriptblock]::Create((irm %s))) -Token %s -CaFingerprint %s",
		psQuote(d.panelURL+"/api/v1/agent/install.ps1"), psQuote(token), psQuote(fingerprint))
}
