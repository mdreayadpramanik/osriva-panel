//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// isolate starts the plugin in its own process group and kills the whole
// group on timeout, so children of a hanging plugin die with it. Children that
// outlive a plugin which answered in time are not killed on Unix (the group id
// could be reused once the plugin is gone).
func isolate(cmd *exec.Cmd) (started func() error, done func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return func() error { return nil }, func() {}
}
