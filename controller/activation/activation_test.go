package activation

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/activation/backend"
)

func TestClientActivateAndValidate(t *testing.T) {
	store, err := backend.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Issue(1, time.Now().Add(time.Hour), "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&backend.Server{Store: store, Proxy: backend.ProxyAccess{
		Host: "gate.decodo.com", Port: 7000, Username: "limited", Password: "sentinel-password", Country: "gb", SessionMinutes: 1440,
	}}).Handler())
	t.Cleanup(srv.Close)
	client := Client{Server: srv.URL}

	act, err := client.Activate(context.Background(), key, "")
	if err != nil {
		t.Fatal(err)
	}
	if act.Proxy.Password != "sentinel-password" || act.InstallationID == "" {
		t.Fatalf("%+v", act)
	}
	dir := t.TempDir()
	fs := FileStore{Path: filepath.Join(dir, "activation.json")}
	if err := fs.Save(Record{Server: srv.URL, InstallationID: act.InstallationID, Token: act.Token, ExpiresAt: act.ExpiresAt}); err != nil {
		t.Fatal(err)
	}
	loaded, err := fs.Load()
	if err != nil || loaded.Token != act.Token {
		t.Fatalf("load %+v %v", loaded, err)
	}
	got, err := client.Validate(context.Background(), loaded.InstallationID, loaded.Token)
	if err != nil || got.Proxy.Username != "limited" {
		t.Fatalf("validate %+v %v", got, err)
	}
	if _, err := client.Activate(context.Background(), "not-a-key", ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("invalid key: %v", err)
	}
	if strings.Contains(errString(client.Activate(context.Background(), "not-a-key", "")), "not-a-key") {
		t.Fatal("client error included the key")
	}
}

func TestClientUnavailable(t *testing.T) {
	client := Client{Server: "http://127.0.0.1:1"}
	_, err := client.Validate(context.Background(), "id", "token")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	_, err = client.Activate(context.Background(), "AAAA-BBBB-CCCC-DDDD", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("activate down: %v", err)
	}
}

func errString(v any, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
