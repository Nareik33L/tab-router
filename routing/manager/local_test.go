package manager

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalProvisionMissingBinary(t *testing.T) {
	dir := t.TempDir()
	l := &Local{DataDir: dir, Binary: filepath.Join(dir, "no-such-tor")}
	_, err := l.Provision(context.Background(), 2)
	if err == nil {
		t.Fatal("expected missing-binary error")
	}
	if strings.Contains(err.Error(), "provider login") {
		t.Fatalf("must not mention login: %v", err)
	}
}

func TestLocalName(t *testing.T) {
	if (&Local{}).Name() != "tor" {
		t.Fatal((&Local{}).Name())
	}
}
