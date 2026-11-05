//go:build windows

package plugins

import (
	"os/exec"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isolate puts the plugin into a job object with "kill on close": on timeout
// the whole job is terminated, and when the call ends any process the plugin
// left behind dies with the job.
//
// The process is assigned right after it starts, so a child it spawns in that
// first instant would escape the job; exec.Cmd offers no suspended start.
func isolate(cmd *exec.Cmd) (started func() error, done func()) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() error { return err }, func() {}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return func() error { return err }, func() {}
	}
	// started runs in the caller's goroutine, Cancel in exec's context watcher.
	var assigned atomic.Bool
	cmd.Cancel = func() error {
		if assigned.Load() {
			return windows.TerminateJobObject(job, 1)
		}
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	started = func() error {
		h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		if err := windows.AssignProcessToJobObject(job, h); err != nil {
			return err
		}
		assigned.Store(true)
		return nil
	}
	return started, func() { _ = windows.CloseHandle(job) }
}
