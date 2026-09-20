package wireguard

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// TestUserspaceLoopback proves two userspace WireGuard stacks can peer on
// localhost and carry TCP without administrator rights or host routes.
func TestUserspaceLoopback(t *testing.T) {
	aPriv, err := GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	bPriv, err := GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	open := func(priv, peer Key, local string) *Tunnel {
		t.Helper()
		tun, err := Open(Config{
			PrivateKey:    priv.Base64(),
			PeerPublicKey: peer.Public().Base64(),
			// Placeholder; replaced with the peer's real listen port below.
			Endpoint:     "127.0.0.1:1",
			LocalAddress: local,
			DNS:          "10.66.66.1",
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tun.Close() })
		return tun
	}
	alice := open(aPriv, bPriv, "10.66.66.1")
	bob := open(bPriv, aPriv, "10.66.66.2")

	portA, err := alice.ListenPort()
	if err != nil || portA == 0 {
		t.Fatalf("alice listen port: %d %v", portA, err)
	}
	portB, err := bob.ListenPort()
	if err != nil || portB == 0 {
		t.Fatalf("bob listen port: %d %v", portB, err)
	}
	if err := alice.SetPeerEndpoint(fmt.Sprintf("127.0.0.1:%d", portB)); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetPeerEndpoint(fmt.Sprintf("127.0.0.1:%d", portA)); err != nil {
		t.Fatal(err)
	}

	ln, err := bob.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.66.66.2").To4(), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	errCh := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer c.Close()
		_, err = io.Copy(c, c)
		errCh <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := alice.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial through tunnel (alice :%d -> bob :%d): %v", portA, portB, err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo mismatch: %q %v", buf, err)
	}
	if !alice.Alive() {
		t.Error("alice reported no handshake after successful dial")
	}
	select {
	case err := <-errCh:
		if err != nil && err != io.EOF {
			t.Errorf("echo server: %v", err)
		}
	default:
	}
}

func TestDialRefusedWhenClosed(t *testing.T) {
	priv, _ := GeneratePrivateKey()
	peer, _ := GeneratePrivateKey()
	tun, err := Open(Config{
		PrivateKey:    priv.Base64(),
		PeerPublicKey: peer.Public().Base64(),
		Endpoint:      "127.0.0.1:1",
		LocalAddress:  "10.66.66.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tun.Close()
	if _, err := tun.DialContext(context.Background(), "tcp", "10.66.66.1:80"); err == nil {
		t.Fatal("dial succeeded on closed tunnel")
	}
	if tun.Alive() {
		t.Error("closed tunnel reported alive")
	}
}
