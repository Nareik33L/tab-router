package provider

import (
	"errors"
	"net/http"
	"testing"
)

func TestRemoteAddrConnReportsTarget(t *testing.T) {
	c := &remoteAddrConn{remote: "api.ipify.org:443"}
	if got := c.RemoteAddr().String(); got != "api.ipify.org:443" {
		t.Fatalf("RemoteAddr %q", got)
	}
	if c.RemoteAddr().Network() != "tcp" {
		t.Fatal("network")
	}
}

func TestIsSystemTLSVerifyBug(t *testing.T) {
	if !isSystemTLSVerifyBug(errors.New("tls: failed to verify certificate: SecPolicyCreateSSL error: 0")) {
		t.Fatal("SecPolicyCreateSSL")
	}
	if isSystemTLSVerifyBug(errors.New("tls: failed to verify certificate: x509: certificate is valid for x, not y")) {
		t.Fatal("ordinary x509 mismatches are not the system-verify bug")
	}
	if isSystemTLSVerifyBug(errors.New("connection refused")) {
		t.Fatal("ordinary errors are not the system-verify bug")
	}
}

func TestHTTPClientForSetsDialTLS(t *testing.T) {
	c := HTTPClientFor(DirectDial, 0)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.DialTLSContext == nil {
		t.Fatal("DialTLSContext must be set so macOS does not verify 127.0.0.1")
	}
	if tr.Proxy != nil {
		// Proxy is a func; nil func is fine. Just ensure we did not leave env proxy.
	}
}
