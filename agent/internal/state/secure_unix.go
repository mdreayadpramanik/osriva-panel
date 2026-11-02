//go:build !windows

package state

import "os"

// secureDir makes the state directory accessible to its owner only.
func secureDir(dir string) (bool, error) {
	if err := os.Chmod(dir, 0o700); err != nil {
		return false, err
	}
	return true, nil
}
