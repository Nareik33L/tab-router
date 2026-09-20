// Package identity manages persistent identity sets: directories holding one
// Chromium profile per identity plus metadata pinning each identity to its
// route slot.
//
//	<data-dir>/identities/current            -> set-<timestamp>  (text pointer)
//	<data-dir>/identities/set-<timestamp>/identity-00N/{profile/, identity.json}
//	<data-dir>/identities/set-<timestamp>/.lock
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Identity is one persistent browsing identity.
type Identity struct {
	ID        string    `json:"id"`         // "identity-001"
	Index     int       `json:"index"`      // 1-based
	RouteSlot int       `json:"route_slot"` // equals Index; pinned for life
	Created   time.Time `json:"created"`
	// Environment is pinned at creation and never changes automatically.
	Environment Environment `json:"environment"`
	Dir         string      `json:"-"`
	Profile     string      `json:"-"`
	JustCreated bool        `json:"-"`
}

// DownloadPath is the absolute download directory for the identity.
func (id Identity) DownloadPath() string { return filepath.Join(id.Dir, id.Environment.DownloadDir) }

// Label is the human form, "Identity 001".
func (id Identity) Label() string { return fmt.Sprintf("Identity %03d", id.Index) }

// Set is a group of identities created together.
type Set struct {
	Name string
	Dir  string
	lock *os.File
}

// Manager owns <data-dir>/identities.
type Manager struct {
	Root string
}

// New returns a Manager rooted at dataDir/identities.
func New(dataDir string) *Manager {
	return &Manager{Root: filepath.Join(dataDir, "identities")}
}

// ErrNoCurrentSet is returned when no set has been created yet.
var ErrNoCurrentSet = errors.New("no identity set exists yet")

func (m *Manager) currentPath() string { return filepath.Join(m.Root, "current") }

// Current returns the active set without locking it.
func (m *Manager) Current() (*Set, error) {
	b, err := os.ReadFile(m.currentPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCurrentSet
		}
		return nil, err
	}
	name := strings.TrimSpace(string(b))
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("identities/current is corrupt: %q", name)
	}
	dir := filepath.Join(m.Root, name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("current identity set %s is missing", name)
	}
	return &Set{Name: name, Dir: dir}, nil
}

// CreateFresh makes a new timestamped set and points current at it. Older
// sets are never touched.
func (m *Manager) CreateFresh() (*Set, error) {
	if err := os.MkdirAll(m.Root, 0o700); err != nil {
		return nil, err
	}
	name := "set-" + time.Now().UTC().Format("2006-01-02T15-04-05Z")
	dir := filepath.Join(m.Root, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(m.Root, fmt.Sprintf("%s-%d", name, i))
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	tmp := m.currentPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(filepath.Base(dir)+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, m.currentPath()); err != nil {
		return nil, err
	}
	return &Set{Name: filepath.Base(dir), Dir: dir}, nil
}

// Open returns the current set, creating one if none exists (or if fresh).
func (m *Manager) Open(fresh bool) (*Set, error) {
	if fresh {
		return m.CreateFresh()
	}
	s, err := m.Current()
	if errors.Is(err, ErrNoCurrentSet) {
		return m.CreateFresh()
	}
	return s, err
}

// ListSets returns all set names, oldest first.
func (m *Manager) ListSets() ([]string, error) {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "set-") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Lock takes an exclusive advisory lock on the set so two controllers never
// open the same profiles. A stale lock from a dead process is reclaimed.
func (s *Set) Lock() error {
	path := filepath.Join(s.Dir, ".lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			s.lock = f
			return nil
		}
		if !os.IsExist(err) {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("identity set %s is locked", s.Name)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && processAlive(pid) {
			return fmt.Errorf("identity set %s is in use by pid %d (run tab-router --stop)", s.Name, pid)
		}
		_ = os.Remove(path)
	}
	return fmt.Errorf("identity set %s is locked", s.Name)
}

// Unlock releases the lock.
func (s *Set) Unlock() {
	if s.lock != nil {
		s.lock.Close()
		_ = os.Remove(filepath.Join(s.Dir, ".lock"))
		s.lock = nil
	}
}

// EnvironmentFor supplies the environment for a newly created identity.
// It is only consulted when an identity does not yet exist.
type EnvironmentFor func(index int) Environment

// Ensure loads or creates identities 1..n in the set. Existing identities are
// never modified; missing ones are created with envFor(index).
func (s *Set) Ensure(n int, envFor EnvironmentFor) ([]Identity, error) {
	if n < 1 {
		return nil, fmt.Errorf("identity count must be >= 1")
	}
	if envFor == nil {
		envFor = func(i int) Environment { return NewEnvironment(i, EnvironmentDefaults{}) }
	}
	out := make([]Identity, 0, n)
	for i := 1; i <= n; i++ {
		id, err := s.loadOrCreate(i, envFor)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// Existing lists identities already present in the set.
func (s *Set) Existing() ([]Identity, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []Identity
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "identity-") {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "identity-"))
		if err != nil {
			continue
		}
		id, err := s.load(idx)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

func (s *Set) dirFor(i int) string { return filepath.Join(s.Dir, fmt.Sprintf("identity-%03d", i)) }

func (s *Set) load(i int) (Identity, error) {
	dir := s.dirFor(i)
	b, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return Identity{}, fmt.Errorf("%s/identity.json: %w", dir, err)
	}
	if id.Index != i {
		return Identity{}, fmt.Errorf("%s/identity.json: index %d does not match directory", dir, id.Index)
	}
	if err := id.Environment.Validate(); err != nil {
		return Identity{}, fmt.Errorf("%s/identity.json: %w", dir, err)
	}
	id.Dir = dir
	id.Profile = filepath.Join(dir, "profile")
	return id, nil
}

func (s *Set) loadOrCreate(i int, envFor EnvironmentFor) (Identity, error) {
	if id, err := s.load(i); err == nil {
		return id, nil
	} else if !os.IsNotExist(err) {
		return Identity{}, err
	}
	dir := s.dirFor(i)
	env := envFor(i)
	if err := env.Validate(); err != nil {
		return Identity{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "profile"), 0o700); err != nil {
		return Identity{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, env.DownloadDir), 0o700); err != nil {
		return Identity{}, err
	}
	id := Identity{ID: fmt.Sprintf("identity-%03d", i), Index: i, RouteSlot: i, Created: time.Now().UTC(), Environment: env}
	b, _ := json.MarshalIndent(id, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), b, 0o600); err != nil {
		return Identity{}, err
	}
	id.Dir = dir
	id.Profile = filepath.Join(dir, "profile")
	id.JustCreated = true
	return id, nil
}
