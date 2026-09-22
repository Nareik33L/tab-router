// Package backend is the activation HTTP API. It lives with the controller
// so unit tests run in CI without a separate service. The desktop binary
// does not embed Decodo credentials; only this process, configured by the
// operator, knows them.
package backend

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

var (
	ErrInvalidKey = errors.New("invalid_key")
	ErrInvalidTok = errors.New("invalid_token")
	ErrExpired    = errors.New("expired")
	ErrRevoked    = errors.New("revoked")
	ErrLimit      = errors.New("installation_limit")
	ErrNoProxy    = errors.New("proxy_unconfigured")
)

// ProxyAccess is what an authorized installation is allowed to use.
// It must be a limited Decodo sub-user, not a secret compiled into clients.
type ProxyAccess struct {
	Host           string
	Port           int
	Username       string
	Password       string
	Country        string
	SessionMinutes int
}

type License struct {
	ID               string    `json:"id"`
	KeyHash          string    `json:"key_hash"`
	Status           string    `json:"status"`
	ExpiresAt        time.Time `json:"expires_at"`
	MaxInstallations int       `json:"max_installations"`
	ProxyUsername    string    `json:"proxy_username,omitempty"`
	ProxyPassword    string    `json:"proxy_password,omitempty"`
}

type Installation struct {
	ID        string    `json:"id"`
	LicenseID string    `json:"license_id"`
	TokenHash string    `json:"token_hash"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	Status    string    `json:"status"`
}

type fileData struct {
	Licenses      []License      `json:"licenses"`
	Installations []Installation `json:"installations"`
}

// Store is the license and installation database.
type Store struct {
	path string
	mu   sync.Mutex
	data fileData
	now  func() time.Time
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

// Issue creates a license and returns the plaintext key once.
// proxyUser and proxyPass, when set, are the limited credentials for this
// license. Empty means the server-wide proxy configuration is used.
func (s *Store) Issue(max int, expires time.Time, proxyUser, proxyPass string) (string, error) {
	if max < 1 {
		max = 1
	}
	key, err := newKey()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Licenses = append(s.data.Licenses, License{
		ID:               newID(),
		KeyHash:          hashSecret(normalizeKey(key)),
		Status:           StatusActive,
		ExpiresAt:        expires.UTC(),
		MaxInstallations: max,
		ProxyUsername:    proxyUser,
		ProxyPassword:    proxyPass,
	})
	return key, s.persist()
}

// Revoke marks a license revoked by plaintext key or license id.
func (s *Store) Revoke(keyOrID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := hashSecret(normalizeKey(keyOrID))
	found := false
	for i := range s.data.Licenses {
		lic := &s.data.Licenses[i]
		if lic.ID == keyOrID || lic.KeyHash == hash {
			lic.Status = StatusRevoked
			found = true
		}
	}
	if !found {
		return ErrInvalidKey
	}
	return s.persist()
}

type Result struct {
	InstallationID string
	Token          string
	Status         string
	ExpiresAt      time.Time
	ProxyUser      string
	ProxyPassword  string
}

func (s *Store) Activate(key, installationID string, fallback ProxyAccess) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lic, err := s.license(hashSecret(normalizeKey(key)))
	if err != nil {
		return Result{}, err
	}
	if err := s.usable(lic); err != nil {
		return Result{}, err
	}
	now := s.now().UTC()
	var inst *Installation
	if installationID != "" {
		for i := range s.data.Installations {
			cand := &s.data.Installations[i]
			if cand.ID == installationID && cand.LicenseID == lic.ID && cand.Status == StatusActive {
				inst = cand
				break
			}
		}
	}
	token, err := newToken()
	if err != nil {
		return Result{}, err
	}
	if inst == nil {
		if s.activeCount(lic.ID) >= lic.MaxInstallations {
			return Result{}, ErrLimit
		}
		s.data.Installations = append(s.data.Installations, Installation{
			ID:        newID(),
			LicenseID: lic.ID,
			TokenHash: hashSecret(token),
			CreatedAt: now,
			LastSeen:  now,
			Status:    StatusActive,
		})
		inst = &s.data.Installations[len(s.data.Installations)-1]
	} else {
		inst.TokenHash = hashSecret(token)
		inst.LastSeen = now
	}
	user, pass, err := s.proxyCreds(lic, fallback)
	if err != nil {
		return Result{}, err
	}
	if err := s.persist(); err != nil {
		return Result{}, err
	}
	return Result{
		InstallationID: inst.ID,
		Token:          token,
		Status:         StatusActive,
		ExpiresAt:      lic.ExpiresAt,
		ProxyUser:      user,
		ProxyPassword:  pass,
	}, nil
}

func (s *Store) Validate(installationID, token string, fallback ProxyAccess) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := s.installation(installationID)
	if inst == nil || inst.TokenHash != hashSecret(token) {
		return Result{}, ErrInvalidTok
	}
	if inst.Status != StatusActive {
		return Result{}, ErrRevoked
	}
	lic := s.licenseByID(inst.LicenseID)
	if lic == nil {
		return Result{}, ErrInvalidTok
	}
	if err := s.usable(lic); err != nil {
		return Result{}, err
	}
	inst.LastSeen = s.now().UTC()
	user, pass, err := s.proxyCreds(lic, fallback)
	if err != nil {
		return Result{}, err
	}
	if err := s.persist(); err != nil {
		return Result{}, err
	}
	return Result{
		InstallationID: inst.ID,
		Token:          token,
		Status:         StatusActive,
		ExpiresAt:      lic.ExpiresAt,
		ProxyUser:      user,
		ProxyPassword:  pass,
	}, nil
}

func (s *Store) Deactivate(installationID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := s.installation(installationID)
	if inst == nil || inst.TokenHash != hashSecret(token) {
		return ErrInvalidTok
	}
	inst.Status = StatusRevoked
	inst.LastSeen = s.now().UTC()
	return s.persist()
}

func (s *Store) license(keyHash string) (*License, error) {
	for i := range s.data.Licenses {
		if s.data.Licenses[i].KeyHash == keyHash {
			return &s.data.Licenses[i], nil
		}
	}
	return nil, ErrInvalidKey
}

func (s *Store) licenseByID(id string) *License {
	for i := range s.data.Licenses {
		if s.data.Licenses[i].ID == id {
			return &s.data.Licenses[i]
		}
	}
	return nil
}

func (s *Store) installation(id string) *Installation {
	for i := range s.data.Installations {
		if s.data.Installations[i].ID == id {
			return &s.data.Installations[i]
		}
	}
	return nil
}

func (s *Store) usable(lic *License) error {
	if lic.Status != StatusActive {
		return ErrRevoked
	}
	if !lic.ExpiresAt.IsZero() && !s.now().Before(lic.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

func (s *Store) activeCount(licenseID string) int {
	n := 0
	for _, inst := range s.data.Installations {
		if inst.LicenseID == licenseID && inst.Status == StatusActive {
			n++
		}
	}
	return n
}

func (s *Store) proxyCreds(lic *License, fallback ProxyAccess) (string, string, error) {
	user, pass := lic.ProxyUsername, lic.ProxyPassword
	if user == "" {
		user = fallback.Username
	}
	if pass == "" {
		pass = fallback.Password
	}
	if user == "" || pass == "" {
		return "", "", ErrNoProxy
	}
	return user, pass, nil
}

func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func normalizeKey(key string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, " ", "")
	key = strings.ReplaceAll(key, "-", "")
	return key
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newKey() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	raw := strings.ToUpper(hex.EncodeToString(b[:]))
	return raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16], nil
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
