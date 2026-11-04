package main

import (
	"os"
	"os/exec"
	"time"
)

// heartbeat is written by a child process every 100 ms; tests watch it to see
// whether the child is still alive after the plugin call.
const heartbeat = "heartbeat"

func runChild() {
	for {
		f, err := os.OpenFile(heartbeat, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write([]byte{'.'})
			_ = f.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// spawnChild starts a copy of this binary as a child and waits until it beats.
func spawnChild() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_ = os.Remove(heartbeat)
	if err := exec.Command(self, "child").Start(); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(heartbeat); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}
