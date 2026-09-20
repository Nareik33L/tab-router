package browser

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// spawnWithPipe starts Chromium with --remote-debugging-pipe on Windows.
//
// Chromium obtains the pipe handles via the C runtime's file-descriptor table
// (_get_osfhandle(3) and (4)). The CRT reconstructs that table from
// STARTUPINFO.lpReserved2, a private structure of the form
//
//	int32 count; byte flags[count]; HANDLE handles[count];
//
// which os/exec cannot populate, so the process is created directly. This is
// the same mechanism Node.js uses for Puppeteer's pipe mode.
func spawnWithPipe(binary string, args, env []string, dir string, stderr *os.File) (*os.Process, Transport, error) {
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}

	// Chromium reads from chromeR (fd 3); we write to ourW.
	var chromeR, ourW windows.Handle
	if err := windows.CreatePipe(&chromeR, &ourW, sa, 0); err != nil {
		return nil, nil, fmt.Errorf("CreatePipe: %w", err)
	}
	// Chromium writes to chromeW (fd 4); we read from ourR.
	var ourR, chromeW windows.Handle
	if err := windows.CreatePipe(&ourR, &chromeW, sa, 0); err != nil {
		windows.CloseHandle(chromeR)
		windows.CloseHandle(ourW)
		return nil, nil, fmt.Errorf("CreatePipe: %w", err)
	}
	// Our ends must not leak into the child.
	_ = windows.SetHandleInformation(ourW, windows.HANDLE_FLAG_INHERIT, 0)
	_ = windows.SetHandleInformation(ourR, windows.HANDLE_FLAG_INHERIT, 0)

	const (
		fopen = 0x01
		fpipe = 0x08
		fdev  = 0x40
		count = 5 // fds 0..4
	)
	std := [3]windows.Handle{
		windows.Handle(os.Stdin.Fd()),
		windows.Handle(os.Stdout.Fd()),
		windows.Handle(os.Stderr.Fd()),
	}
	if stderr != nil {
		std[2] = windows.Handle(stderr.Fd())
	}
	handles := [count]windows.Handle{std[0], std[1], std[2], chromeR, chromeW}
	flags := [count]byte{fopen | fdev, fopen | fdev, fopen | fdev, fopen | fpipe, fopen | fpipe}
	for i := 0; i < 3; i++ {
		if handles[i] == 0 || handles[i] == windows.InvalidHandle {
			handles[i] = windows.InvalidHandle
			flags[i] = 0
		} else {
			_ = windows.SetHandleInformation(handles[i], windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
		}
	}
	reserved := make([]byte, 4+count+count*int(unsafe.Sizeof(windows.Handle(0))))
	*(*int32)(unsafe.Pointer(&reserved[0])) = count
	copy(reserved[4:], flags[:])
	hp := unsafe.Pointer(&reserved[4+count])
	for i := 0; i < count; i++ {
		*(*windows.Handle)(unsafe.Add(hp, i*int(unsafe.Sizeof(windows.Handle(0))))) = handles[i]
	}

	si := &startupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Reserved2 = &reserved[0]
	si.CbReserved2 = uint16(len(reserved))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = handles[0], handles[1], handles[2]

	cmdline := windows.ComposeCommandLine(append([]string{binary}, args...))
	cmdlineP, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return nil, nil, err
	}
	binP, err := windows.UTF16PtrFromString(binary)
	if err != nil {
		return nil, nil, err
	}
	var dirP *uint16
	if dir != "" {
		if dirP, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, nil, err
		}
	}
	envBlock, err := createEnvBlock(env)
	if err != nil {
		return nil, nil, err
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(binP, cmdlineP, nil, nil, true,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NEW_PROCESS_GROUP, envBlock, dirP,
		(*windows.StartupInfo)(unsafe.Pointer(si)), &pi)
	windows.CloseHandle(chromeR)
	windows.CloseHandle(chromeW)
	if err != nil {
		windows.CloseHandle(ourR)
		windows.CloseHandle(ourW)
		return nil, nil, fmt.Errorf("CreateProcess: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	proc, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		windows.CloseHandle(ourR)
		windows.CloseHandle(ourW)
		return nil, nil, err
	}
	r := os.NewFile(uintptr(ourR), "cdp-read")
	w := os.NewFile(uintptr(ourW), "cdp-write")
	return proc, newPipeTransport(r, w), nil
}

// startupInfo mirrors STARTUPINFOW including the CRT-private lpReserved2
// fields that golang.org/x/sys keeps unexported.
type startupInfo struct {
	Cb            uint32
	Reserved      *uint16
	Desktop       *uint16
	Title         *uint16
	X             uint32
	Y             uint32
	XSize         uint32
	YSize         uint32
	XCountChars   uint32
	YCountChars   uint32
	FillAttribute uint32
	Flags         uint32
	ShowWindow    uint16
	CbReserved2   uint16
	Reserved2     *byte
	StdInput      windows.Handle
	StdOutput     windows.Handle
	StdErr        windows.Handle
}

func createEnvBlock(env []string) (*uint16, error) {
	if len(env) == 0 {
		return nil, nil
	}
	var block []uint16
	for _, kv := range env {
		u, err := syscall.UTF16FromString(kv)
		if err != nil {
			return nil, err
		}
		block = append(block, u...)
	}
	block = append(block, 0)
	return &block[0], nil
}
