// Package wireguard is a userspace WireGuard tunnel (wireguard-go + gvisor
// netstack). It needs no administrator rights and no host routing-table
// changes: Dial talks through the tunnel, UDP to the peer is sent by this
// process, and Chromium never sees the protocol.
package wireguard

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// Key is a 32-byte WireGuard key.
type Key [32]byte

// GeneratePrivateKey returns a clamped Curve25519 private key.
func GeneratePrivateKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return k, err
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return k, nil
}

// Public returns the corresponding public key.
func (k Key) Public() Key {
	var pub, priv [32]byte
	copy(priv[:], k[:])
	curve25519.ScalarBaseMult(&pub, &priv)
	return Key(pub)
}

// Hex is the UAPI encoding.
func (k Key) Hex() string { return hex.EncodeToString(k[:]) }

// Base64 is the encoding Mullvad (and most .conf files) use.
func (k Key) Base64() string { return base64.StdEncoding.EncodeToString(k[:]) }

// ParseKey accepts hex or standard base64.
func ParseKey(s string) (Key, error) {
	var k Key
	if s == "" {
		return k, fmt.Errorf("wireguard: empty key")
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		copy(k[:], b)
		return k, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return k, fmt.Errorf("wireguard: key must be 32-byte hex or base64")
	}
	copy(k[:], b)
	return k, nil
}
