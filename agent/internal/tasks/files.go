package tasks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

var (
	guardMu sync.RWMutex
	guard   = func() *FSGuard {
		p, g := DefaultDeny("")
		return NewFSGuard(DefaultRoots(), p, g)
	}()
)

// ConfigureFiles sets the file policy (called once at agent start).
func ConfigureFiles(roots []string, agentDir string) {
	if len(roots) == 0 {
		roots = DefaultRoots()
	}
	p, g := DefaultDeny(agentDir)
	guardMu.Lock()
	guard = NewFSGuard(roots, p, g)
	guardMu.Unlock()
}

func currentGuard() *FSGuard {
	guardMu.RLock()
	defer guardMu.RUnlock()
	return guard
}

func entryType(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "dir"
	case m.IsRegular():
		return "file"
	}
	return "other"
}

func filesList(_ context.Context, g *FSGuard, path string) (any, error) {
	real, err := g.Check(path)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil, err
	}
	res := agenttask.FilesListResult{Path: real, Entries: []agenttask.FileEntry{}, Roots: g.Roots()}
	if !g.IsRoot(real) {
		if parent := filepath.Dir(real); parent != real {
			if _, err := g.Check(parent); err == nil {
				res.Parent = parent
			}
		}
	}
	for _, e := range ents {
		full := filepath.Join(real, e.Name())
		fe := agenttask.FileEntry{Name: e.Name()}
		if info, err := os.Lstat(full); err == nil {
			fe.Type, fe.Size, fe.Mode = entryType(info.Mode()), info.Size(), info.Mode().String()
			fe.ModTime = info.ModTime().UTC().Format(time.RFC3339)
		}
		if r, err := resolve(full); err == nil && g.Denied(r) {
			fe.Denied = true
		}
		res.Entries = append(res.Entries, fe)
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		a, b := res.Entries[i], res.Entries[j]
		if (a.Type == "dir") != (b.Type == "dir") {
			return a.Type == "dir"
		}
		return a.Name < b.Name
	})
	return res, nil
}

func filesRead(_ context.Context, g *FSGuard, path string) (any, error) {
	real, err := g.Check(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	buf, err := io.ReadAll(io.LimitReader(f, agenttask.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	res := agenttask.FilesReadResult{Path: real, Size: info.Size(), ModTime: info.ModTime().UTC().Format(time.RFC3339)}
	if len(buf) > agenttask.MaxFileBytes {
		buf, res.Truncated = buf[:agenttask.MaxFileBytes], true
	}
	// Text only: anything with NUL bytes or invalid UTF-8 is reported as binary, not mangled.
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(trimPartialRune(buf, res.Truncated)) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(buf)
	return res, nil
}

// trimPartialRune drops a multi-byte character cut by truncation.
func trimPartialRune(b []byte, truncated bool) []byte {
	if !truncated {
		return b
	}
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return b
}

func filesWrite(_ context.Context, g *FSGuard, p agenttask.FilesWriteParams) (any, error) {
	real, err := g.Check(p.Path)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o644)
	created := false
	switch info, err := os.Lstat(real); {
	case err == nil && !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", p.Path)
	case err == nil:
		mode = info.Mode().Perm()
	case errors.Is(err, os.ErrNotExist) && p.Create:
		created = true
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%s does not exist (set create to make a new file)", p.Path)
	default:
		return nil, err
	}
	// Atomic replace: a crash never leaves a half-written config file.
	tmp, err := os.CreateTemp(filepath.Dir(real), ".osriva-write-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(p.Content); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	_ = os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), real); err != nil {
		return nil, err
	}
	return agenttask.FilesWriteResult{Path: real, Size: int64(len(p.Content)), Created: created}, nil
}

func filesMkdir(_ context.Context, g *FSGuard, path string) (any, error) {
	real, err := g.Check(path)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(real, 0o755); err != nil {
		return nil, err
	}
	return map[string]string{"path": real}, nil
}

func filesDelete(_ context.Context, g *FSGuard, path string) (any, error) {
	real, err := g.Check(path)
	if err != nil {
		return nil, err
	}
	if g.IsRoot(real) {
		return nil, fmt.Errorf("%w: an allowed root cannot be deleted", ErrPathDenied)
	}
	info, err := os.Lstat(real)
	if err != nil {
		return nil, err
	}
	// os.Remove deletes a file or an EMPTY directory only: no recursive deletes.
	if err := os.Remove(real); err != nil {
		if info.IsDir() {
			return nil, fmt.Errorf("only empty directories can be deleted: %w", err)
		}
		return nil, err
	}
	return map[string]string{"path": real}, nil
}
