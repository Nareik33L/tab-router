//go:build !darwin

package provider

import "crypto/x509"

func extraRootCAs() *x509.CertPool { return nil }
