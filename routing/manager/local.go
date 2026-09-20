package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/tor"
)

// Local provisions one independent Tor process per identity. There is no
// user account, no Tab Router login, and no cloud credential file.
// Chromium still talks only to its Gate; it never sees Tor.
//
// Each process has its own SOCKS port so the exit fingerprint observed at
// V1 can be pinned (ExitNodes + StrictNodes) for the rest of the session.
// Reestablish never issues NEWNYM and never mints new SOCKS credentials:
// a new circuit or a new username would rotate the public IP.
type Local struct {
	DataDir string
	Binary  string
	Log     io.Writer

	mu    sync.Mutex
	slots map[int]*localSlot
}

type localSlot struct {
	daemon *tor.Daemon
	def    provider.RouteDef
}

func (l *Local) Name() string { return "tor" }

func (l *Local) log() io.Writer {
	if l.Log != nil {
		return l.Log
	}
	return os.Stderr
}

// Provision starts n local tor daemons (the Expert Bundle is downloaded on
// first use) and returns n SOCKS5 routes with distinct credentials.
func (l *Local) Provision(ctx context.Context, n int) ([]provider.Route, error) {
	if n < 1 {
		return nil, fmt.Errorf("provision: need at least 1 route")
	}
	if err := l.ensureBinary(); err != nil {
		return nil, err
	}
	type outcome struct {
		slot int
		sl   *localSlot
		r    provider.Route
		err  error
	}
	ch := make(chan outcome, n)
	for i := 1; i <= n; i++ {
		go func(slot int) {
			sl, r, err := l.bootSlot(ctx, slot)
			ch <- outcome{slot: slot, sl: sl, r: r, err: err}
		}(i)
	}
	slots := make(map[int]*localSlot, n)
	routes := make([]provider.Route, n)
	var firstErr error
	for i := 0; i < n; i++ {
		o := <-ch
		if o.err != nil && firstErr == nil {
			firstErr = o.err
		}
		if o.sl != nil {
			slots[o.slot] = o.sl
		}
		if o.r != nil {
			routes[o.slot-1] = o.r
		}
	}
	l.mu.Lock()
	l.slots = slots
	l.mu.Unlock()
	if firstErr != nil {
		_ = l.Close()
		return nil, firstErr
	}
	return routes, nil
}

// PinExit implements SessionExits: lock this slot to the exit already used
// by socksUser so the session public IP cannot rotate.
func (l *Local) PinExit(ctx context.Context, slot int, socksUser string) error {
	l.mu.Lock()
	sl, ok := l.slots[slot]
	l.mu.Unlock()
	if !ok || sl == nil || sl.daemon == nil {
		return fmt.Errorf("pin exit: no tor daemon for slot %d", slot)
	}
	if socksUser == "" {
		socksUser = sl.def.Username
	}
	return sl.daemon.PinSOCKSUser(ctx, socksUser)
}

func (l *Local) Reestablish(ctx context.Context, slot int, failed provider.Route) (provider.Route, error) {
	if slot < 1 {
		return nil, fmt.Errorf("provision: no route for slot %d", slot)
	}
	l.mu.Lock()
	sl, ok := l.slots[slot]
	l.mu.Unlock()
	if !ok || sl == nil {
		sl, r, err := l.bootSlot(ctx, slot)
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.slots == nil {
			l.slots = map[int]*localSlot{}
		}
		l.slots[slot] = sl
		l.mu.Unlock()
		return r, nil
	}
	if err := sl.daemon.ReapplyPin(); err != nil {
		return l.restartPinned(ctx, slot, sl)
	}
	if sl.daemon.SocksAddr != "" {
		sl.def.Address = sl.daemon.SocksAddr
	}
	return provider.New(sl.def)
}

func (l *Local) restartPinned(ctx context.Context, slot int, sl *localSlot) (provider.Route, error) {
	pinned := sl.daemon.PinnedExit()
	user, pass := sl.def.Username, sl.def.Password
	_ = sl.daemon.Stop()
	next, r, err := l.bootSlot(ctx, slot)
	if err != nil {
		return nil, err
	}
	if user != "" {
		next.def.Username = user
		next.def.Password = pass
		r, err = provider.New(next.def)
		if err != nil {
			_ = next.daemon.Stop()
			return nil, err
		}
	}
	if pinned != "" {
		if err := next.daemon.SetPinnedExit(pinned); err != nil {
			_ = next.daemon.Stop()
			return nil, err
		}
	}
	l.mu.Lock()
	l.slots[slot] = next
	l.mu.Unlock()
	return r, nil
}

func (l *Local) Close() error {
	l.mu.Lock()
	slots := l.slots
	l.slots = nil
	l.mu.Unlock()
	var first error
	for _, sl := range slots {
		if sl != nil && sl.daemon != nil {
			if err := sl.daemon.Stop(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (l *Local) ensureBinary() error {
	if l.Binary != "" {
		return nil
	}
	bin, err := tor.Install(torRoot(l.DataDir), l.log())
	if err != nil {
		return fmt.Errorf("local exits: %w", err)
	}
	l.Binary = bin
	return nil
}

func (l *Local) bootSlot(ctx context.Context, slot int) (*localSlot, provider.Route, error) {
	d := &tor.Daemon{
		Binary:  l.Binary,
		DataDir: filepath.Join(torRoot(l.DataDir), fmt.Sprintf("id-%03d", slot)),
		Log:     l.log(),
	}
	if err := d.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("local exits: %w", err)
	}
	def := sessionRouteDef(slot, d.SocksAddr)
	r, err := provider.New(def)
	if err != nil {
		_ = d.Stop()
		return nil, nil, err
	}
	return &localSlot{daemon: d, def: def}, r, nil
}

func sessionRouteDef(slot int, socksAddr string) provider.RouteDef {
	return provider.RouteDef{
		ID:       fmt.Sprintf("route-%03d", slot),
		Type:     "socks5",
		Address:  socksAddr,
		Username: fmt.Sprintf("identity-%03d-%s", slot, nonce(6)),
		Password: nonce(8),
	}
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
