//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func isWindows() bool { return true }

// BinaryPath is %ProgramFiles%\OSRIVA\osriva-agent.exe.
func BinaryPath() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "OSRIVA", "osriva-agent.exe")
}

func connect() (*mgr.Mgr, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return nil, errors.New("managing the service requires an elevated (Run as Administrator) prompt")
	}
	return mgr.Connect()
}

func Install(o Options) error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists; uninstall it first", Name)
	}
	bin := BinaryPath()
	if err := installBinary(o.Source, bin); err != nil {
		return err
	}
	s, err := m.CreateService(Name, bin, mgr.Config{
		DisplayName:      DisplayName,
		Description:      Description,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, WindowsArgs(o.ConfigDir)...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	// Restart after crashes, like Restart=on-failure on systemd.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400)
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func Uninstall() error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return nil // not installed
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err == nil {
			for i := 0; i < 30; i++ {
				if st, err := s.Query(); err != nil || st.State == svc.Stopped {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	return s.Delete()
}

func Query() (Status, error) {
	m, err := mgr.Connect()
	if err != nil {
		return StatusUnknown, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return StatusNotInstalled, nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return StatusUnknown, err
	}
	if st.State == svc.Running {
		return StatusRunning, nil
	}
	return StatusStopped, nil
}
