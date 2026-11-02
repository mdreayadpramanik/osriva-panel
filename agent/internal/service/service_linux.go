//go:build linux

package service

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

const (
	binPath    = "/usr/local/bin/osriva-agent"
	systemdDir = "/etc/systemd/system"
	unitPath   = systemdDir + "/osriva-agent.service"
	openrcPath = "/etc/init.d/osriva-agent"
)

func isWindows() bool { return false }

func hasSystemd() bool { _, err := os.Stat("/run/systemd/system"); return err == nil }

func hasOpenRC() bool {
	_, err := exec.LookPath("openrc-run")
	return err == nil
}

// BinaryPath is where Install places the agent binary.
func BinaryPath() string { return binPath }

func Install(o Options) error {
	if os.Geteuid() != 0 {
		return errors.New("installing the service requires root (run with sudo)")
	}
	switch {
	case hasSystemd():
		if err := installBinary(o.Source, binPath); err != nil {
			return err
		}
		if err := writeFile(unitPath, SystemdUnit(binPath, o.ConfigDir), 0o644); err != nil {
			return err
		}
		if err := run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		return run("systemctl", "enable", "--now", Name+".service")
	case hasOpenRC():
		if err := installBinary(o.Source, binPath); err != nil {
			return err
		}
		if err := writeFile(openrcPath, OpenRCScript(binPath, o.ConfigDir), 0o755); err != nil {
			return err
		}
		if err := run("rc-update", "add", Name, "default"); err != nil {
			return err
		}
		return run("rc-service", Name, "start")
	}
	return errors.New("no supported service manager found (systemd or OpenRC); run `osriva-agent run` under your own supervisor")
}

func Uninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("removing the service requires root (run with sudo)")
	}
	switch {
	case fileExists(unitPath):
		_ = run("systemctl", "disable", "--now", Name+".service")
		if err := os.Remove(unitPath); err != nil {
			return err
		}
		return run("systemctl", "daemon-reload")
	case fileExists(openrcPath):
		_ = run("rc-service", Name, "stop")
		_ = run("rc-update", "del", Name, "default")
		return os.Remove(openrcPath)
	}
	return nil
}

func Query() (Status, error) {
	switch {
	case fileExists(unitPath):
		out, _ := exec.Command("systemctl", "is-active", Name+".service").Output()
		if strings.TrimSpace(string(out)) == "active" {
			return StatusRunning, nil
		}
		return StatusStopped, nil
	case fileExists(openrcPath):
		if exec.Command("rc-service", Name, "status").Run() == nil {
			return StatusRunning, nil
		}
		return StatusStopped, nil
	}
	return StatusNotInstalled, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
