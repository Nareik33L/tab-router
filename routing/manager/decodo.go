package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/routing/platform"
	"github.com/Nareik33L/tab-router/routing/provider"
)

const (
	// Decodo's current SOCKS5 gateway. Country endpoints such as
	// us.decodo.com do not speak SOCKS5; location belongs in the username.
	// https://help.decodo.com/docs/residential-proxy-protocols
	defaultDecodoHost = "gate.decodo.com"
	defaultDecodoPort = 7000

	// 1440 minutes is Decodo's maximum sticky duration (24 hours).
	// https://help.decodo.com/docs/residential-proxy-custom-sticky-sessions
	defaultSessionMinutes = 1440
	maxSessionMinutes     = 1440
)

// ErrDecodoCredentials means Decodo was selected but no account password
// is available. Callers must fail closed and must not fall through to Tor.
var ErrDecodoCredentials = errors.New("decodo credentials are not available")

// DecodoConfig is the non-secret plus authorized-secret input for a
// Decodo provisioner. Password is never written to config.toml.
type DecodoConfig struct {
	Account        string
	Password       string
	Host           string
	Port           int
	Country        string
	SessionMinutes int
	// StatePath persists session IDs (not passwords) so a restart inside
	// the sticky window reuses the same residential session.
	StatePath string
	Log       io.Writer
}

// Decodo creates one sticky residential SOCKS5 route per identity.
// Provision(n) accepts any n >= 1. The product cap (MaxIdentities) is
// enforced by config, not here.
//
// Each slot gets its own Decodo session id. Reestablish reconnects that
// same id. It does not mint a new session, because a new id is how Decodo
// hands out a different residential IP.
//
// The username uses session_iplock, not session. Decodo's session parameter
// rotates to a new IP when the residential peer drops; session_iplock
// fails the request instead, which is what fail-closed needs.
type Decodo struct {
	Account        string
	Password       string
	Host           string
	Port           int
	Country        string
	SessionMinutes int
	StatePath      string
	Log            io.Writer

	mu    sync.Mutex
	slots map[int]decodoSlot
}

type decodoSlot struct {
	SessionID string
	Created   time.Time
}

type decodoStateFile struct {
	Slots []decodoStateSlot `json:"slots"`
}

type decodoStateSlot struct {
	Slot      int       `json:"slot"`
	SessionID string    `json:"session_id"`
	Created   time.Time `json:"created_at"`
}

// NewDecodo validates cfg and fills gateway defaults. It does not open a
// connection and does not embed a password of its own.
func NewDecodo(cfg DecodoConfig) (*Decodo, error) {
	d := &Decodo{
		Account:        strings.TrimPrefix(strings.TrimSpace(cfg.Account), "user-"),
		Password:       cfg.Password,
		Host:           strings.TrimSpace(cfg.Host),
		Port:           cfg.Port,
		Country:        strings.ToLower(strings.TrimSpace(cfg.Country)),
		SessionMinutes: cfg.SessionMinutes,
		StatePath:      cfg.StatePath,
		Log:            cfg.Log,
		slots:          map[int]decodoSlot{},
	}
	if d.Host == "" {
		d.Host = defaultDecodoHost
	}
	if d.Port == 0 {
		d.Port = defaultDecodoPort
	}
	if d.SessionMinutes == 0 {
		d.SessionMinutes = defaultSessionMinutes
	}
	if err := d.validate(); err != nil {
		return nil, err
	}
	return d, nil
}

// DecodoFromEnv builds a provisioner from DECODO_USERNAME and
// DECODO_PASSWORD. Host, port, country, and session length come from cfg
// when set, otherwise from DECODO_HOST, DECODO_PORT, DECODO_COUNTRY, and
// DECODO_SESSION_MINUTES. Missing username or password returns
// ErrDecodoCredentials.
func DecodoFromEnv(dataDir string, cfg DecodoConfig) (*Decodo, error) {
	if cfg.Account == "" {
		cfg.Account = os.Getenv("DECODO_USERNAME")
	}
	if cfg.Password == "" {
		cfg.Password = os.Getenv("DECODO_PASSWORD")
	}
	if strings.TrimSpace(cfg.Account) == "" || cfg.Password == "" {
		return nil, ErrDecodoCredentials
	}
	if v := os.Getenv("DECODO_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("DECODO_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("decodo: DECODO_PORT: %w", err)
		}
		cfg.Port = p
	}
	if v := os.Getenv("DECODO_COUNTRY"); v != "" {
		cfg.Country = v
	}
	if v := os.Getenv("DECODO_SESSION_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("decodo: DECODO_SESSION_MINUTES: %w", err)
		}
		cfg.SessionMinutes = n
	}
	if cfg.StatePath == "" && dataDir != "" {
		cfg.StatePath = decodoStatePath(dataDir)
	}
	return NewDecodo(cfg)
}

func decodoStatePath(dataDir string) string {
	return filepath.Join(dataDir, "decodo-sessions.json")
}

func (d *Decodo) Name() string { return "decodo" }

func (d *Decodo) validate() error {
	if d.Account == "" || d.Password == "" {
		return ErrDecodoCredentials
	}
	if d.SessionMinutes < 1 || d.SessionMinutes > maxSessionMinutes {
		return fmt.Errorf("decodo: session minutes must be 1..%d", maxSessionMinutes)
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("decodo: invalid port %d", d.Port)
	}
	if _, err := Username(d.Account, d.Country, "session", d.SessionMinutes); err != nil {
		return err
	}
	return nil
}

// Provision returns n independent sticky sessions, slot i+1 → routes[i].
func (d *Decodo) Provision(ctx context.Context, n int) ([]provider.Route, error) {
	if n < 1 {
		return nil, fmt.Errorf("provision: need at least 1 route")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.load(); err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]provider.Route, 0, n)
	for slot := 1; slot <= n; slot++ {
		d.ensure(slot, now)
		r, err := d.route(slot)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
		d.logf("decodo route %d created\n", slot)
	}
	if err := d.save(); err != nil {
		return nil, err
	}
	return out, nil
}

// Reestablish reconnects the slot's existing Decodo session. It never
// generates a new session id.
func (d *Decodo) Reestablish(ctx context.Context, slot int, failed provider.Route) (provider.Route, error) {
	if slot < 1 {
		return nil, fmt.Errorf("decodo: no route for slot %d", slot)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.load(); err != nil {
		return nil, err
	}
	st, ok := d.slots[slot]
	if !ok || st.SessionID == "" {
		return nil, fmt.Errorf("decodo: no session for slot %d", slot)
	}
	return d.route(slot)
}

// ReplaceSession mints a new session id for slot. Startup uses this only
// when two identities observed the same public IP. Reestablish does not.
func (d *Decodo) ReplaceSession(ctx context.Context, slot int) (provider.Route, error) {
	if slot < 1 {
		return nil, fmt.Errorf("decodo: no route for slot %d", slot)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.load(); err != nil {
		return nil, err
	}
	d.slots[slot] = decodoSlot{SessionID: newSessionID(), Created: time.Now()}
	if err := d.save(); err != nil {
		return nil, err
	}
	d.logf("decodo route %d session replaced\n", slot)
	return d.route(slot)
}

func (d *Decodo) Close() error { return nil }

// SessionID reports the sticky session id for slot, or "" if unknown.
func (d *Decodo) SessionID(slot int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.slots[slot].SessionID
}

func (d *Decodo) ensure(slot int, now time.Time) {
	st, ok := d.slots[slot]
	if ok && st.SessionID != "" && now.Before(st.Created.Add(d.life())) {
		return
	}
	d.slots[slot] = decodoSlot{SessionID: newSessionID(), Created: now}
}

func (d *Decodo) life() time.Duration {
	return time.Duration(d.SessionMinutes) * time.Minute
}

func (d *Decodo) route(slot int) (provider.Route, error) {
	st := d.slots[slot]
	user, err := Username(d.Account, d.Country, st.SessionID, d.SessionMinutes)
	if err != nil {
		return nil, err
	}
	return provider.New(provider.RouteDef{
		ID:       fmt.Sprintf("decodo-%03d", slot),
		Type:     "socks5",
		Address:  net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Username: user,
		Password: d.Password,
	})
}

func (d *Decodo) logf(format string, args ...any) {
	if d.Log == nil {
		return
	}
	fmt.Fprintf(d.Log, format, args...)
}

func (d *Decodo) load() error {
	if d.slots == nil {
		d.slots = map[int]decodoSlot{}
	}
	if d.StatePath == "" {
		return nil
	}
	b, err := os.ReadFile(d.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	var file decodoStateFile
	if err := json.Unmarshal(b, &file); err != nil {
		return fmt.Errorf("decodo: session state: %w", err)
	}
	for _, s := range file.Slots {
		if s.Slot < 1 || s.SessionID == "" {
			continue
		}
		d.slots[s.Slot] = decodoSlot{SessionID: s.SessionID, Created: s.Created}
	}
	return nil
}

func (d *Decodo) save() error {
	if d.StatePath == "" {
		return nil
	}
	file := decodoStateFile{}
	for slot, st := range d.slots {
		file.Slots = append(file.Slots, decodoStateSlot{Slot: slot, SessionID: st.SessionID, Created: st.Created})
	}
	// Stable order so tests and diffs do not flap.
	for i := 1; i < len(file.Slots); i++ {
		j := i
		for j > 0 && file.Slots[j].Slot < file.Slots[j-1].Slot {
			file.Slots[j], file.Slots[j-1] = file.Slots[j-1], file.Slots[j]
			j--
		}
	}
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(d.StatePath), 0o700); err != nil {
		return err
	}
	tmp := d.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := platform.Current().SecureFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, d.StatePath)
}

// Username builds a Decodo SOCKS5 username.
//
//	user-ACCOUNT[-country-cc]-session_iplock-SESSION-sessionduration-MINUTES
//
// session_iplock is the current Decodo parameter that keeps one residential
// IP for the sticky window and fails instead of rotating when that IP dies.
func Username(account, country, sessionID string, minutes int) (string, error) {
	account = strings.TrimPrefix(strings.TrimSpace(account), "user-")
	country = strings.ToLower(strings.TrimSpace(country))
	sessionID = strings.TrimSpace(sessionID)
	if !alphaNum(account) {
		return "", fmt.Errorf("decodo: account must be alphanumeric")
	}
	if !alphaNum(sessionID) {
		return "", fmt.Errorf("decodo: session id must be alphanumeric")
	}
	if country != "" && (len(country) != 2 || !alphaNum(country)) {
		return "", fmt.Errorf("decodo: country must be a 2-letter code")
	}
	if minutes < 1 || minutes > maxSessionMinutes {
		return "", fmt.Errorf("decodo: session minutes must be 1..%d", maxSessionMinutes)
	}
	var b strings.Builder
	b.WriteString("user-")
	b.WriteString(account)
	if country != "" {
		b.WriteString("-country-")
		b.WriteString(country)
	}
	b.WriteString("-session_iplock-")
	b.WriteString(sessionID)
	b.WriteString("-sessionduration-")
	b.WriteString(strconv.Itoa(minutes))
	return b.String(), nil
}

func alphaNum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
