package activation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Nareik33L/tab-router/routing/platform"
)

// Record is the local installation token. It does not contain the Decodo
// password; that is fetched again on each successful validate.
type Record struct {
	Server         string    `json:"server"`
	InstallationID string    `json:"installation_id"`
	Token          string    `json:"token"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// SecretStore persists an installation token.
type SecretStore interface {
	Save(Record) error
	Load() (Record, error)
	Clear() error
}

// FileStore is the non-keychain store. The file is owner-only, same as
// other Tab Router credential files. Darwin uses the login keychain instead.
type FileStore struct {
	Path string
}

func (f FileStore) Save(rec Record) error {
	if f.Path == "" {
		return errors.New("activation store path is empty")
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := platform.Current().SecureFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, f.Path)
}

func (f FileStore) Load() (Record, error) {
	var rec Record
	b, err := os.ReadFile(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return rec, ErrNotActivated
		}
		return rec, err
	}
	if err := platform.Current().CheckFilePrivate(f.Path); err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	if rec.InstallationID == "" || rec.Token == "" {
		return rec, ErrNotActivated
	}
	return rec, nil
}

func (f FileStore) Clear() error {
	err := os.Remove(f.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// FilePath is the fallback token file inside the data directory.
func FilePath(dataDir string) string {
	return filepath.Join(dataDir, "activation.json")
}
