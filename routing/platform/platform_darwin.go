package platform

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

// macOS provider (milestone M8). Endpoint enumeration shells out to lsof,
// which ships with macOS; a libproc implementation can replace it later
// without changing the interface.

var current Provider = darwinProvider{}

type darwinProvider struct{ unixIPC }

func (darwinProvider) Name() string { return "macos" }

func (darwinProvider) DefaultDataDir() (string, error) { return userDataDir("tab-router") }

func (p darwinProvider) KillTree(root int) error { return killTree(p, root) }

func (darwinProvider) ProcessTree(root int) ([]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	children := map[int][]int{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	tree := []int{root}
	for i := 0; i < len(tree); i++ {
		tree = append(tree, children[tree[i]]...)
	}
	return tree, nil
}

// RemoteEndpoints parses `lsof -nP -a -i -p pid,... -F pPTn`.
func (darwinProvider) RemoteEndpoints(pids []int) ([]Endpoint, error) {
	if len(pids) == 0 {
		return nil, nil
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	cmd := exec.Command("lsof", "-nP", "-a", "-i", "-p", strings.Join(list, ","), "-F", "pPTn")
	out, err := cmd.Output()
	if err != nil {
		// lsof exits 1 when no matching files exist; that is "no endpoints".
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && len(out) == 0 {
			return nil, nil
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("lsof: %w", err)
		}
	}
	var eps []Endpoint
	var cur Endpoint
	var have bool
	flush := func() {
		if have && (cur.Proto != "" && cur.Local.IsValid()) {
			if !(strings.HasPrefix(cur.Proto, "tcp") && cur.State == "LISTEN") {
				eps = append(eps, cur)
			}
		}
		have = false
		cur = Endpoint{PID: cur.PID}
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if len(line) == 0 {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			flush()
			cur.PID, _ = strconv.Atoi(val)
		case 'P':
			flush()
			cur.Proto = strings.ToLower(val)
			have = true
		case 'T':
			if strings.HasPrefix(val, "ST=") {
				cur.State = strings.TrimPrefix(val, "ST=")
			}
		case 'n':
			local, remote := splitLsofName(val)
			cur.Local = parseLsofAddr(local)
			cur.Remote = parseLsofAddr(remote)
			if cur.Local.Addr().Is6() && cur.Proto == "tcp" {
				cur.Proto = "tcp6"
			} else if cur.Local.Addr().Is6() && cur.Proto == "udp" {
				cur.Proto = "udp6"
			}
		}
	}
	flush()
	return eps, nil
}

func splitLsofName(s string) (string, string) {
	if i := strings.Index(s, "->"); i >= 0 {
		return s[:i], s[i+2:]
	}
	return s, ""
}

func parseLsofAddr(s string) netip.AddrPort {
	if s == "" {
		return netip.AddrPort{}
	}
	s = strings.ReplaceAll(s, "*", "0.0.0.0")
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap
	}
	// "[::1]:80" or "::1:80" forms
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.AddrPort{}
	}
	host := strings.Trim(s[:i], "[]")
	port, err := strconv.ParseUint(s[i+1:], 10, 16)
	if err != nil {
		return netip.AddrPort{}
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(addr, uint16(port))
}
