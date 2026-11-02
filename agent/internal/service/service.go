package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUnsupported is returned on platforms without a service adapter.
var ErrUnsupported = errors.New("installing the agent as a service is not supported on this platform yet")

// Status is a coarse service state.
type Status string

const (
	StatusRunning      Status = "running"
	StatusStopped      Status = "stopped"
	StatusNotInstalled Status = "not-installed"
	StatusUnknown      Status = "unknown"
)

// Options for Install.
type Options struct {
	ConfigDir string
	// Source is the agent binary to install (default: the running executable).
	Source string
}

// run executes a platform tool with explicit arguments (never through a shell).
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installBinary copies src to dst atomically with mode 0755, unless they are the same file.
func installBinary(src, dst string) error {
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		src = exe
	}
	if a, err := filepath.EvalSymlinks(src); err == nil {
		src = a
	}
	if b, err := filepath.EvalSymlinks(dst); err == nil && strings.EqualFold(b, src) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".osriva-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil && !isWindows() {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
