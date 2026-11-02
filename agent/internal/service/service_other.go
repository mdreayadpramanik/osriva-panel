//go:build !linux && !freebsd && !windows

package service

func isWindows() bool { return false }

func BinaryPath() string { return "" }

func Install(Options) error { return ErrUnsupported }

func Uninstall() error { return ErrUnsupported }

func Query() (Status, error) { return StatusUnknown, ErrUnsupported }
