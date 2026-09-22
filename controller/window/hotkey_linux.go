//go:build linux

package window

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func startShortcuts(next, prev Shortcut, onNext, onPrev func()) (func(), error) {
	conn, root, minKC, maxKC, err := dialX()
	if err != nil {
		return nil, err
	}
	mapping, err := keyboardMapping(conn, minKC, maxKC)
	if err != nil {
		conn.Close()
		return nil, err
	}
	type grab struct {
		code byte
		fire func()
	}
	var grabs []grab
	if !next.Empty() {
		code, err := keycode(mapping, minKC, next.Key)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if err := grabKey(conn, root, code, mods(next)); err != nil {
			conn.Close()
			return nil, err
		}
		grabs = append(grabs, grab{code: code, fire: onNext})
	}
	if !prev.Empty() {
		code, err := keycode(mapping, minKC, prev.Key)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if err := grabKey(conn, root, code, mods(prev)); err != nil {
			conn.Close()
			return nil, err
		}
		grabs = append(grabs, grab{code: code, fire: onPrev})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32)
		for {
			_ = conn.SetReadDeadline(time.Time{})
			if _, err := readFull(conn, buf); err != nil {
				return
			}
			if buf[0] != 2 { // KeyPress
				continue
			}
			code := buf[1]
			for _, g := range grabs {
				if g.code == code && g.fire != nil {
					g.fire()
				}
			}
		}
	}()
	stop := func() {
		_ = conn.Close()
		<-done
	}
	return stop, nil
}

func mods(s Shortcut) uint16 {
	var m uint16
	if s.Shift {
		m |= 1
	}
	if s.Ctrl {
		m |= 4
	}
	if s.Alt {
		m |= 8
	}
	if s.Super {
		m |= 64
	}
	return m
}

func keycode(mapping [][]uint32, minKC byte, key string) (byte, error) {
	want := keysym(key)
	if want == 0 {
		return 0, fmt.Errorf("no keysym for %q", key)
	}
	for i, syms := range mapping {
		for _, sym := range syms {
			if sym == want {
				return minKC + byte(i), nil
			}
		}
	}
	return 0, fmt.Errorf("key %q is not on this keyboard", key)
}

func keysym(key string) uint32 {
	switch key {
	case "space":
		return 0x0020
	case "tab":
		return 0xff09
	case "enter":
		return 0xff0d
	case "escape":
		return 0xff1b
	case "left":
		return 0xff51
	case "up":
		return 0xff52
	case "right":
		return 0xff53
	case "down":
		return 0xff54
	case "minus":
		return 0x002d
	case "equal":
		return 0x003d
	case "bracketleft":
		return 0x005b
	case "bracketright":
		return 0x005d
	case "semicolon":
		return 0x003b
	case "quote":
		return 0x0027
	case "comma":
		return 0x002c
	case "period":
		return 0x002e
	case "slash":
		return 0x002f
	case "backslash":
		return 0x005c
	case "grave":
		return 0x0060
	}
	if len(key) == 1 && key[0] >= 'a' && key[0] <= 'z' {
		return uint32(key[0])
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		return uint32(key[0])
	}
	if len(key) >= 2 && key[0] == 'f' {
		n := 0
		for _, c := range key[1:] {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 12 {
			return 0xffbe + uint32(n-1)
		}
	}
	return 0
}

func dialX() (net.Conn, uint32, byte, byte, error) {
	host, num, err := parseDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		return nil, 0, 0, 0, err
	}
	var conn net.Conn
	if host == "" || host == "unix" {
		conn, err = net.Dial("unix", "/tmp/.X11-unix/X"+num)
	} else {
		port := 6000
		if n, e := atoiErr(num); e == nil {
			port += n
		}
		conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", host, port))
	}
	if err != nil {
		return nil, 0, 0, 0, err
	}
	name, data := readXAuthority(num)
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		conn.Close()
		return nil, 0, 0, 0, err
	}
	if _, err := conn.Write(handshake(name, data)); err != nil {
		conn.Close()
		return nil, 0, 0, 0, err
	}
	root, minKC, maxKC, err := readSetup(conn)
	if err != nil {
		conn.Close()
		return nil, 0, 0, 0, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, root, minKC, maxKC, nil
}

func parseDisplay(d string) (host, num string, err error) {
	if strings.TrimSpace(d) == "" {
		return "", "", errors.New("DISPLAY is not set")
	}
	host, rest, ok := strings.Cut(d, ":")
	if !ok {
		return "", "", fmt.Errorf("DISPLAY %q is not valid", d)
	}
	num, _, _ = strings.Cut(rest, ".")
	if num == "" {
		num = "0"
	}
	return host, num, nil
}

func handshake(name string, data []byte) []byte {
	buf := make([]byte, 12+len(name)+pad(len(name))+len(data)+pad(len(data)))
	buf[0] = 'l'
	binary.LittleEndian.PutUint16(buf[2:], 11)
	binary.LittleEndian.PutUint16(buf[6:], uint16(len(name)))
	binary.LittleEndian.PutUint16(buf[8:], uint16(len(data)))
	copy(buf[12:], name)
	copy(buf[12+len(name)+pad(len(name)):], data)
	return buf
}

func pad(n int) int { return (4 - n%4) % 4 }

func readSetup(conn net.Conn) (root uint32, minKC, maxKC byte, err error) {
	head := make([]byte, 8)
	if _, err = readFull(conn, head); err != nil {
		return 0, 0, 0, err
	}
	if head[0] != 1 {
		return 0, 0, 0, errors.New("X server refused the connection")
	}
	n := int(binary.LittleEndian.Uint16(head[6:])) * 4
	body := make([]byte, n)
	if _, err = readFull(conn, body); err != nil {
		return 0, 0, 0, err
	}
	msg := append(head, body...)
	if len(msg) < 40 {
		return 0, 0, 0, errors.New("short X setup")
	}
	minKC = msg[34]
	maxKC = msg[35]
	vendorLen := int(binary.LittleEndian.Uint16(msg[24:]))
	formats := int(msg[29])
	off := 40 + vendorLen + pad(vendorLen) + formats*8
	if off+4 > len(msg) {
		return 0, 0, 0, errors.New("X setup has no screen")
	}
	root = binary.LittleEndian.Uint32(msg[off:])
	return root, minKC, maxKC, nil
}

func keyboardMapping(conn net.Conn, minKC, maxKC byte) ([][]uint32, error) {
	count := int(maxKC) - int(minKC) + 1
	req := make([]byte, 8)
	req[0] = 101
	binary.LittleEndian.PutUint16(req[2:], 2)
	req[4] = minKC
	req[5] = byte(count)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	head := make([]byte, 32)
	if _, err := readFull(conn, head); err != nil {
		return nil, err
	}
	if head[0] == 0 {
		return nil, errors.New("X server rejected the keyboard map")
	}
	per := int(head[1])
	extra := int(binary.LittleEndian.Uint32(head[4:])) * 4
	payload := make([]byte, extra)
	if extra > 0 {
		if _, err := readFull(conn, payload); err != nil {
			return nil, err
		}
	}
	out := make([][]uint32, count)
	for i := 0; i < count; i++ {
		out[i] = make([]uint32, per)
		for j := 0; j < per; j++ {
			off := (i*per + j) * 4
			if off+4 > len(payload) {
				break
			}
			out[i][j] = binary.LittleEndian.Uint32(payload[off:])
		}
	}
	return out, nil
}

func grabKey(conn net.Conn, root uint32, code byte, modifiers uint16) error {
	// Also grab with CapsLock and NumLock held, or those LEDs swallow the binding.
	for _, extra := range []uint16{0, 2, 16, 18} {
		req := make([]byte, 16)
		req[0] = 33
		binary.LittleEndian.PutUint16(req[2:], 4)
		binary.LittleEndian.PutUint32(req[4:], root)
		binary.LittleEndian.PutUint16(req[8:], modifiers|extra)
		req[10] = code
		req[11] = 1 // Async
		req[12] = 1 // Async
		if _, err := conn.Write(req); err != nil {
			return err
		}
	}
	return nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		n, err := conn.Read(buf[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}

func atoiErr(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// readXAuthority returns the MIT-MAGIC-COOKIE for display number, or empty
// auth when the file is absent (some local servers still accept that).
func readXAuthority(displayNum string) (string, []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	name, data := parseXAuthority(b, displayNum)
	return name, data
}

func parseXAuthority(b []byte, displayNum string) (string, []byte) {
	var wildName string
	var wildData []byte
	for len(b) >= 4 {
		family := binary.BigEndian.Uint16(b[0:])
		addrLen := int(binary.BigEndian.Uint16(b[2:]))
		if len(b) < 4+addrLen+2 {
			break
		}
		b = b[4+addrLen:]
		numLen := int(binary.BigEndian.Uint16(b[0:]))
		if len(b) < 2+numLen+2 {
			break
		}
		num := string(b[2 : 2+numLen])
		b = b[2+numLen:]
		nameLen := int(binary.BigEndian.Uint16(b[0:]))
		if len(b) < 2+nameLen+2 {
			break
		}
		name := string(b[2 : 2+nameLen])
		b = b[2+nameLen:]
		dataLen := int(binary.BigEndian.Uint16(b[0:]))
		if len(b) < 2+dataLen {
			break
		}
		data := append([]byte(nil), b[2:2+dataLen]...)
		b = b[2+dataLen:]
		if num == displayNum && (family == 256 || family == 65535 || family == 0) {
			return name, data
		}
		if family == 65535 && wildName == "" {
			wildName, wildData = name, data
		}
	}
	return wildName, wildData
}
