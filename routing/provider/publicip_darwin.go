//go:build darwin

package provider

import (
	"crypto/x509"
	"os/exec"
	"sync"
)

var extraRootsOnce sync.Once
var extraRoots *x509.CertPool

// extraRootCAs loads macOS system roots as PEMs so crypto/x509 can verify
// without Security.framework (which fails SecPolicyCreateSSL on SOCKS
// connections whose RemoteAddr is 127.0.0.1).
func extraRootCAs() *x509.CertPool {
	extraRootsOnce.Do(func() {
		pool := x509.NewCertPool()
		ok := false
		for _, path := range []string{
			"/System/Library/Keychains/SystemRootCertificates.keychain",
			"/Library/Keychains/SystemRootCertificates.keychain",
		} {
			out, err := exec.Command("security", "find-certificate", "-a", "-p", path).Output()
			if err != nil || len(out) == 0 {
				continue
			}
			if pool.AppendCertsFromPEM(out) {
				ok = true
			}
		}
		if ok {
			extraRoots = pool
		}
	})
	return extraRoots
}
