package platform

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Windows provider (milestone M7). Endpoint enumeration uses iphlpapi's
// owner-PID tables; IPC uses a named pipe whose DACL admits only the
// current user.

var current Provider = windowsProvider{}

type windowsProvider struct{}

func (windowsProvider) Name() string { return "windows" }

func (windowsProvider) DefaultDataDir() (string, error) { return userDataDir("tab-router") }

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1
)

func (windowsProvider) ProcessTree(root int) ([]int, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	children := map[int][]int{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil, err
	}
	for {
		children[int(pe.ParentProcessID)] = append(children[int(pe.ParentProcessID)], int(pe.ProcessID))
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	tree := []int{root}
	for i := 0; i < len(tree); i++ {
		tree = append(tree, children[tree[i]]...)
	}
	return tree, nil
}

func (windowsProvider) KillTree(root int) error {
	pids, _ := windowsProvider{}.ProcessTree(root)
	for i := len(pids) - 1; i >= 0; i-- {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pids[i]))
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
	return nil
}

func (windowsProvider) RemoteEndpoints(pids []int) ([]Endpoint, error) {
	want := map[int]bool{}
	for _, p := range pids {
		want[p] = true
	}
	var out []Endpoint
	for _, fam := range []uint32{windows.AF_INET, windows.AF_INET6} {
		buf, err := getTable(procGetExtendedTcpTable, fam, tcpTableOwnerPidAll)
		if err != nil {
			return nil, err
		}
		out = append(out, decodeTCP(buf, fam, want)...)
		buf, err = getTable(procGetExtendedUdpTable, fam, udpTableOwnerPid)
		if err != nil {
			return nil, err
		}
		out = append(out, decodeUDP(buf, fam, want)...)
	}
	return out, nil
}

func getTable(proc *windows.LazyProc, family uint32, class uint32) ([]byte, error) {
	var size uint32
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(class), 0)
	if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && r != 0 {
		return nil, fmt.Errorf("%s: error %d", proc.Name, r)
	}
	for i := 0; i < 4; i++ {
		buf := make([]byte, size)
		r, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(class), 0)
		if r == 0 {
			return buf, nil
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, fmt.Errorf("%s: error %d", proc.Name, r)
		}
	}
	return nil, fmt.Errorf("%s: table kept growing", proc.Name)
}

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

// Port fields hold the port in network order in the low 16 bits.
func netPort(v uint32) uint16 { return uint16(v&0xff)<<8 | uint16(v>>8&0xff) }

func addr4(b []byte) netip.Addr { return netip.AddrFrom4([4]byte{b[0], b[1], b[2], b[3]}) }
func addr6(b []byte) netip.Addr {
	var a [16]byte
	copy(a[:], b[:16])
	return netip.AddrFrom16(a)
}

func decodeTCP(buf []byte, fam uint32, want map[int]bool) []Endpoint {
	if len(buf) < 4 {
		return nil
	}
	n := int(le32(buf[0:4]))
	var out []Endpoint
	if fam == windows.AF_INET {
		const rowSize = 24
		for i := 0; i < n && 4+(i+1)*rowSize <= len(buf); i++ {
			row := buf[4+i*rowSize:]
			pid := int(le32(row[20:24]))
			if !want[pid] {
				continue
			}
			state := tcpStates[int(le32(row[0:4]))]
			if state == "LISTEN" {
				continue
			}
			out = append(out, Endpoint{
				PID: pid, Proto: "tcp", State: state,
				Local:  netip.AddrPortFrom(addr4(row[4:8]), netPort(le32(row[8:12]))),
				Remote: netip.AddrPortFrom(addr4(row[12:16]), netPort(le32(row[16:20]))),
			})
		}
		return out
	}
	const rowSize = 56
	for i := 0; i < n && 4+(i+1)*rowSize <= len(buf); i++ {
		row := buf[4+i*rowSize:]
		pid := int(le32(row[52:56]))
		if !want[pid] {
			continue
		}
		state := tcpStates[int(le32(row[48:52]))]
		if state == "LISTEN" {
			continue
		}
		out = append(out, Endpoint{
			PID: pid, Proto: "tcp6", State: state,
			Local:  netip.AddrPortFrom(addr6(row[0:16]), netPort(le32(row[20:24]))),
			Remote: netip.AddrPortFrom(addr6(row[24:40]), netPort(le32(row[44:48]))),
		})
	}
	return out
}

func decodeUDP(buf []byte, fam uint32, want map[int]bool) []Endpoint {
	if len(buf) < 4 {
		return nil
	}
	n := int(le32(buf[0:4]))
	var out []Endpoint
	if fam == windows.AF_INET {
		const rowSize = 12
		for i := 0; i < n && 4+(i+1)*rowSize <= len(buf); i++ {
			row := buf[4+i*rowSize:]
			pid := int(le32(row[8:12]))
			if !want[pid] {
				continue
			}
			out = append(out, Endpoint{PID: pid, Proto: "udp",
				Local: netip.AddrPortFrom(addr4(row[0:4]), netPort(le32(row[4:8])))})
		}
		return out
	}
	const rowSize = 28
	for i := 0; i < n && 4+(i+1)*rowSize <= len(buf); i++ {
		row := buf[4+i*rowSize:]
		pid := int(le32(row[24:28]))
		if !want[pid] {
			continue
		}
		out = append(out, Endpoint{PID: pid, Proto: "udp6",
			Local: netip.AddrPortFrom(addr6(row[0:16]), netPort(le32(row[20:24])))})
	}
	return out
}

func currentUserSID() (string, error) {
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

func (windowsProvider) ListenIPC(dir, name string) (net.Listener, string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, "", err
	}
	// Owner-only DACL, protected from inheritance.
	sddl := fmt.Sprintf("D:P(A;;GA;;;%s)", sid)
	pipe := `\\.\pipe\` + name + "-" + sanitize(filepath.Base(dir))
	ln, err := winio.ListenPipe(pipe, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		return nil, "", err
	}
	return ln, "pipe:" + pipe, nil
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

func (windowsProvider) DialIPC(endpoint string) (net.Conn, error) {
	return winio.DialPipe(strings.TrimPrefix(endpoint, "pipe:"), nil)
}

// SecureFile replaces the DACL with owner-only full control.
func (windowsProvider) SecureFile(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;FA;;;%s)", sid))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

// CheckFilePrivate verifies no ACE grants access to anyone other than the
// current user, SYSTEM or Administrators.
func (windowsProvider) CheckFilePrivate(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	sddl := sd.String()
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	// Coarse check: reject well-known broad principals.
	for _, bad := range []string{";;;WD)", ";;;BU)", ";;;AU)", ";;;IU)", ";;;AN)", ";;;S-1-1-0)"} {
		if strings.Contains(sddl, bad) {
			return fmt.Errorf("%s grants access to a broad group (%s); restrict it to %s", path, bad, sid)
		}
	}
	return nil
}
