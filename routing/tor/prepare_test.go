package tor

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestPrepareMissingBinary(t *testing.T) {
	err := Prepare(filepath.Join(t.TempDir(), "no-such-tor"))
	if runtime.GOOS != "darwin" {
		if err != nil {
			t.Fatalf("non-darwin Prepare is a no-op: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("expected error for a missing binary")
	}
}

func TestPrepareConcurrent(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tor")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Prepare(bin)
		}()
	}
	wg.Wait()
	close(errs)
	if runtime.GOOS != "darwin" {
		for err := range errs {
			if err != nil {
				t.Fatalf("non-darwin Prepare is a no-op: %v", err)
			}
		}
		return
	}
	// On macOS a missing dummy file should fail, but never panic or
	// interleave codesign on the same path.
	for err := range errs {
		if err == nil {
			t.Fatal("expected error for a missing binary")
		}
	}
}
