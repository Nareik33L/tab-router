// Package socks5 implements the subset of RFC 1928/1929 that Tab Router needs:
// a CONNECT-only client dialer (used to reach upstream proxies) and a
// CONNECT-only server (used by the per-identity gate and by the test
// infrastructure). UDP ASSOCIATE and BIND are deliberately unsupported: no
// UDP may ever leave the browser.
package socks5

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

const (
	Version5 = 0x05

	MethodNoAuth       = 0x00
	MethodUserPass     = 0x02
	MethodNoAcceptable = 0xFF

	CmdConnect      = 0x01
	CmdBind         = 0x02
	CmdUDPAssociate = 0x03

	ATypIPv4   = 0x01
	ATypDomain = 0x03
	ATypIPv6   = 0x04

	RepSuccess              = 0x00
	RepGeneralFailure       = 0x01
	RepNotAllowed           = 0x02
	RepNetworkUnreachable   = 0x03
	RepHostUnreachable      = 0x04
	RepConnectionRefused    = 0x05
	RepTTLExpired           = 0x06
	RepCommandNotSupported  = 0x07
	RepAddrTypeNotSupported = 0x08
)

// AddrType is the SOCKS5 address type observed in a request. It is the
// signal the gate uses to detect local DNS resolution: a browser that sends
// IPv4/IPv6 for what the user typed as a hostname has resolved it locally.
type AddrType byte

func (a AddrType) String() string {
	switch byte(a) {
	case ATypIPv4:
		return "IPV4"
	case ATypDomain:
		return "DOMAINNAME"
	case ATypIPv6:
		return "IPV6"
	}
	return fmt.Sprintf("ATYP(%d)", byte(a))
}

// Target is a parsed SOCKS5 destination.
type Target struct {
	Type AddrType
	Host string // hostname or textual IP
	Port int
}

func (t Target) HostPort() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

var errShort = errors.New("socks5: short read")

// readTarget parses ATYP + address + port from r.
func readTarget(r io.Reader) (Target, error) {
	var atyp [1]byte
	if _, err := io.ReadFull(r, atyp[:]); err != nil {
		return Target{}, err
	}
	t := Target{Type: AddrType(atyp[0])}
	switch atyp[0] {
	case ATypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return t, err
		}
		t.Host = net.IP(b[:]).String()
	case ATypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return t, err
		}
		t.Host = net.IP(b[:]).String()
	case ATypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return t, err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return t, err
		}
		t.Host = string(b)
	default:
		return t, fmt.Errorf("socks5: unsupported address type %d", atyp[0])
	}
	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return t, err
	}
	t.Port = int(p[0])<<8 | int(p[1])
	return t, nil
}

// appendTarget encodes host:port. Hostnames are always sent as DOMAINNAME so
// that resolution happens at the far end; IP literals keep their type.
func appendTarget(buf []byte, host string, port int) ([]byte, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buf = append(buf, ATypIPv4)
			buf = append(buf, v4...)
		} else {
			buf = append(buf, ATypIPv6)
			buf = append(buf, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, fmt.Errorf("socks5: invalid hostname length %d", len(host))
		}
		buf = append(buf, ATypDomain, byte(len(host)))
		buf = append(buf, host...)
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("socks5: invalid port %d", port)
	}
	return append(buf, byte(port>>8), byte(port)), nil
}

func splitHostPort(hostport string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("socks5: bad port %q", portStr)
	}
	return host, port, nil
}
