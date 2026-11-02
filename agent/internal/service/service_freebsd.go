//go:build freebsd

package service

import (
	"errors"
	"os"
	"os/exec"
)

const (
	binPath = "/usr/local/bin/osriva-agent"
	rcPath  = "/usr/local/etc/rc.d/osriva_agent"
	rcName  = "osriva_agent"
)

func isWindows() bool { return false }

func BinaryPath() string { return binPath }

func Install(o Options) error {
	if os.Geteuid() != 0 {
		return errors.New("installing the service requires root")
	}
	if err := installBinary(o.Source, binPath); err != nil {
		return err
	}
	if err := writeFile(rcPath, FreeBSDRCScript(binPath, o.ConfigDir), 0o755); err != nil {
		return err
	}
	if err := run("sysrc", rcName+"_enable=YES"); err != nil {
		return err
	}
	return run("service", rcName, "start")
}

func Uninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("removing the service requires root")
	}
	if _, err := os.Stat(rcPath); err != nil {
		return nil
	}
	_ = run("service", rcName, "stop")
	_ = run("sysrc", "-x", rcName+"_enable")
	return os.Remove(rcPath)
}

func Query() (Status, error) {
	if _, err := os.Stat(rcPath); err != nil {
		return StatusNotInstalled, nil
	}
	if exec.Command("service", rcName, "status").Run() == nil {
		return StatusRunning, nil
	}
	return StatusStopped, nil
}
