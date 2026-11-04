//go:build windows

package plugins

import "os/exec"

// isolate kills the plugin process on timeout (exec's default). Windows has no
// process groups here; a job object is not used yet.
func isolate(cmd *exec.Cmd) {}
