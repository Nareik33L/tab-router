//go:build !windows

package tor

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
