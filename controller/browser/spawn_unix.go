//go:build !windows

package browser

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnWithPipe starts Chromium with the DevTools pipe on fds 3 (Chromium
// reads) and 4 (Chromium writes), in its own process group so the whole
// tree can be killed at once.
func spawnWithPipe(binary string, args, env []string, dir string, stderr *os.File) (*os.Process, Transport, error) {
	chromeR, ourW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	ourR, chromeW, err := os.Pipe()
	if err != nil {
		chromeR.Close()
		ourW.Close()
		return nil, nil, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{chromeR, chromeW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	chromeR.Close()
	chromeW.Close()
	if err != nil {
		ourR.Close()
		ourW.Close()
		return nil, nil, err
	}
	return cmd.Process, newPipeTransport(ourR, ourW), nil
}
