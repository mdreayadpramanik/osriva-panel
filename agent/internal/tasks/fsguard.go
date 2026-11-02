package tasks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrPathDenied is returned for any path outside the allowed roots or on the deny list.
var ErrPathDenied = errors.New("path is not allowed by this agent's file policy")

// FSGuard confines file operations. The policy (roots and deny list) is local to
// the agent: the control plane cannot widen it.
type FSGuard struct {
	roots []string // resolved, cleaned
	deny  []string // resolved prefixes (a file or a whole directory)
	globs []string // filepath.Match patterns on resolved paths
}

// DefaultRoots are the directories the file manager may access when the agent
// configuration does not name any.
func DefaultRoots() []string {
	if runtime.GOOS == "windows" {
		return []string{`C:\inetpub`, `C:\ProgramData`}
	}
	return []string{"/etc", "/srv", "/var/www", "/var/log", "/opt", "/home"}
}

// DefaultDeny lists secrets that stay unreadable even inside a root.
func DefaultDeny(agentDir string) (prefixes, globs []string) {
	prefixes = []string{agentDir}
	if runtime.GOOS == "windows" {
		prefixes = append(prefixes, `C:\ProgramData\Microsoft\Crypto`, `C:\ProgramData\Microsoft\Windows\WER`)
		return prefixes, nil
	}
	prefixes = append(prefixes, "/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-",
		"/etc/sudoers", "/etc/sudoers.d", "/etc/osriva", "/etc/ssl/private")
	globs = []string{"/etc/ssh/ssh_host_*_key", "/home/*/.ssh/id_*", "/home/*/.gnupg"}
	return prefixes, globs
}

func norm(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// resolve returns the real path (symlinks resolved). For a path that does not
// exist yet, the parent is resolved and the final element appended.
func resolve(p string) (string, error) {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(p)), nil
}

func NewFSGuard(roots, denyPrefixes, denyGlobs []string) *FSGuard {
	g := &FSGuard{globs: denyGlobs}
	for _, r := range roots {
		if real, err := filepath.EvalSymlinks(filepath.Clean(r)); err == nil {
			g.roots = append(g.roots, real)
		}
	}
	for _, d := range denyPrefixes {
		if d == "" {
			continue
		}
		if real, err := resolve(filepath.Clean(d)); err == nil {
			g.deny = append(g.deny, real)
		} else {
			g.deny = append(g.deny, filepath.Clean(d))
		}
	}
	return g
}

func within(base, p string) bool {
	rel, err := filepath.Rel(norm(base), norm(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Roots returns the allowed roots that exist on this host.
func (g *FSGuard) Roots() []string { return append([]string(nil), g.roots...) }

// IsRoot reports whether p is one of the roots themselves.
func (g *FSGuard) IsRoot(p string) bool {
	for _, r := range g.roots {
		if norm(r) == norm(p) {
			return true
		}
	}
	return false
}

func (g *FSGuard) denied(real string) bool {
	for _, d := range g.deny {
		if within(d, real) {
			return true
		}
	}
	for _, pat := range g.globs {
		// A match on the path or any parent denies it (e.g. a whole .gnupg directory).
		for p := real; ; p = filepath.Dir(p) {
			if ok, _ := filepath.Match(pat, p); ok {
				return true
			}
			if parent := filepath.Dir(p); parent == p {
				break
			}
		}
	}
	return false
}

// Check resolves p and returns its real path if the policy allows it.
func (g *FSGuard) Check(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: path must be absolute", ErrPathDenied)
	}
	// Lexical check first: a path that is plainly outside every root is refused
	// before the file system is touched at all (no existence or permission leaks).
	lexical := filepath.Clean(p)
	inside := false
	for _, r := range g.roots {
		if within(r, lexical) {
			inside = true
			break
		}
	}
	if !inside || g.denied(lexical) {
		return "", fmt.Errorf("%w: %s is outside the allowed directories (%s)", ErrPathDenied, p, strings.Join(g.roots, ", "))
	}
	// Then resolve symlinks and check the real path again.
	real, err := resolve(lexical)
	if err != nil {
		return "", err
	}
	if g.denied(real) {
		return "", fmt.Errorf("%w: %s", ErrPathDenied, p)
	}
	for _, r := range g.roots {
		if within(r, real) {
			return real, nil
		}
	}
	return "", fmt.Errorf("%w: %s is outside the allowed directories (%s)", ErrPathDenied, p, strings.Join(g.roots, ", "))
}

// Denied reports whether an already-resolved path is on the deny list (for listings).
func (g *FSGuard) Denied(real string) bool { return g.denied(real) }
