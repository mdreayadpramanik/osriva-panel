//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// isolate starts the plugin in its own process group and kills the whole
// group on timeout, so children of a plugin cannot outlive the call.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
