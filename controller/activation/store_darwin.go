//go:build darwin

package activation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func Open(dataDir string) SecretStore {
	return KeychainStore{Service: "tab-router", Account: "activation"}
}

// KeychainStore keeps the installation token in the macOS login keychain.
type KeychainStore struct {
	Service string
	Account string
}

func (k KeychainStore) Save(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_ = k.Clear()
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", k.Service, "-a", k.Account, "-w", string(b))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return execErr(out, err)
	}
	return nil
}

func (k KeychainStore) Load() (Record, error) {
	var rec Record
	cmd := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", k.Account, "-w")
	out, err := cmd.Output()
	if err != nil {
		return rec, ErrNotActivated
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &rec); err != nil {
		return rec, err
	}
	if rec.InstallationID == "" || rec.Token == "" {
		return rec, ErrNotActivated
	}
	return rec, nil
}

func (k KeychainStore) Clear() error {
	cmd := exec.Command("security", "delete-generic-password", "-s", k.Service, "-a", k.Account)
	_, _ = cmd.CombinedOutput()
	return nil
}

func execErr(out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}
