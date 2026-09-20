package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"path/filepath"

	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/tor"
)

// Local provisions one independent Tor circuit per identity. There is no
// user account, no Tab Router login, and no cloud credential file.
// Chromium still talks only to its Gate; it never sees Tor.
type Local struct {
	DataDir string
	Binary  string
	Log     io.Writer

	daemon *tor.Daemon
}

func (l *Local) Name() string { return "tor" }

func (l *Local) log() io.Writer {
	if l.Log != nil {
		return l.Log
	}
	return os.Stderr
}

// Provision starts a local tor daemon (downloaded on first use) and returns
// n SOCKS5 routes with distinct IsolateSOCKSAuth credentials.
func (l *Local) Provision(ctx context.Context, n int) ([]provider.Route, error) {
	if n < 1 {
		return nil, fmt.Errorf("provision: need at least 1 route")
	}
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	out := make([]provider.Route, 0, n)
	for i := 0; i < n; i++ {
		r, err := l.route(i + 1)
		if err != nil {
			_ = l.Close()
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (l *Local) Reestablish(ctx context.Context, slot int, failed provider.Route) (provider.Route, error) {
	if slot < 1 {
		return nil, fmt.Errorf("provision: no route for slot %d", slot)
	}
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	_ = l.daemon.Newnym(ctx)
	return l.route(slot)
}

func (l *Local) Close() error {
	if l.daemon != nil {
		err := l.daemon.Stop()
		l.daemon = nil
		return err
	}
	return nil
}

func (l *Local) ensure(ctx context.Context) error {
	if l.daemon != nil {
		return nil
	}
	bin := l.Binary
	if bin == "" {
		root := torRoot(l.DataDir)
		var err error
		bin, err = tor.Install(root, l.log())
		if err != nil {
			return fmt.Errorf("local exits: %w", err)
		}
	}
	d := &tor.Daemon{Binary: bin, DataDir: torRoot(l.DataDir), Log: l.log()}
	if err := d.Start(ctx); err != nil {
		return fmt.Errorf("local exits: %w", err)
	}
	l.daemon = d
	return nil
}

func (l *Local) route(slot int) (provider.Route, error) {
	user := fmt.Sprintf("identity-%03d-%s", slot, nonce(6))
	def := provider.RouteDef{
		ID:       fmt.Sprintf("route-%03d", slot),
		Type:     "socks5",
		Address:  l.daemon.SocksAddr,
		Username: user,
		Password: nonce(8),
	}
	return provider.New(def)
}

func torRoot(dataDir string) string {
	return filepath.Join(dataDir, "tor")
}

func nonce(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", n)
	}
	return hex.EncodeToString(b)
}
