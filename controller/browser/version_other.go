//go:build !windows

package browser

import "os/exec"

func hideWindow(*exec.Cmd) {}
