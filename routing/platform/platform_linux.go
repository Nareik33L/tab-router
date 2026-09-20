package platform

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var current Provider = linuxProvider{}

type linuxProvider struct{ unixIPC }

func (linuxProvider) Name() string { return "linux (dev/CI only)" }

func (linuxProvider) DefaultDataDir() (string, error) { return userDataDir("tab-router") }

func (p linuxProvider) KillTree(root int) error { return killTree(p, root) }

func (linuxProvider) ProcessTree(root int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, ok := readPPid(pid)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	out := []int{root}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out, nil
}

func readPPid(pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	// comm may contain spaces; it ends at the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 > len(s) {
		return 0, false
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	return ppid, err == nil
}

func (linuxProvider) RemoteEndpoints(pids []int) ([]Endpoint, error) {
	inodes := map[uint64]int{}
	for _, pid := range pids {
		fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process exited or not ours
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
				n, err := strconv.ParseUint(link[8:len(link)-1], 10, 64)
				if err == nil {
					inodes[n] = pid
				}
			}
		}
	}
	if len(inodes) == 0 {
		return nil, nil
	}
	var out []Endpoint
	for _, tbl := range []string{"tcp", "tcp6", "udp", "udp6"} {
		eps, err := readProcNet(tbl, inodes)
		if err != nil {
			return nil, err
		}
		out = append(out, eps...)
	}
	return out, nil
}

func readProcNet(table string, inodes map[uint64]int) ([]Endpoint, error) {
	f, err := os.Open("/proc/net/" + table)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Endpoint
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		pid, ok := inodes[inode]
		if !ok {
			continue
		}
		local, err := parseProcAddr(fields[1])
		if err != nil {
			continue
		}
		remote, err := parseProcAddr(fields[2])
		if err != nil {
			continue
		}
		st, _ := strconv.ParseInt(fields[3], 16, 32)
		ep := Endpoint{PID: pid, Proto: table, Local: local, Remote: remote}
		if strings.HasPrefix(table, "tcp") {
			ep.State = tcpStates[int(st)]
			if ep.State == "LISTEN" {
				continue
			}
		}
		out = append(out, ep)
	}
	return out, sc.Err()
}

// parseProcAddr decodes /proc/net "HEXADDR:HEXPORT". IPv4 is one
// little-endian 32-bit word; IPv6 is four little-endian 32-bit words.
func parseProcAddr(s string) (netip.AddrPort, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.AddrPort{}, fmt.Errorf("bad addr %q", s)
	}
	raw, err := hex.DecodeString(s[:i])
	if err != nil {
		return netip.AddrPort{}, err
	}
	port, err := strconv.ParseUint(s[i+1:], 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	for w := 0; w+4 <= len(raw); w += 4 {
		raw[w], raw[w+1], raw[w+2], raw[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w]
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("bad addr %q", s)
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return netip.AddrPortFrom(addr, uint16(port)), nil
}
