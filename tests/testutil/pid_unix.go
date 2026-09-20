//go:build !windows

package testutil

import (
	"os"
	"syscall"
)

// PIDAlive reports whether pid exists.
func PIDAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
