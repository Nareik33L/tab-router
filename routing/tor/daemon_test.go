package tor

import (
	"strings"
	"testing"
)

func TestWithLibPathPrepends(t *testing.T) {
	env := withLibPath([]string{"PATH=/bin", "LD_LIBRARY_PATH=/old"}, "/opt/tor")
	var ld, dy string
	for _, e := range env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			ld = e
		}
		if strings.HasPrefix(e, "DYLD_LIBRARY_PATH=") {
			dy = e
		}
	}
	if !strings.HasPrefix(ld, "LD_LIBRARY_PATH=/opt/tor") || !strings.Contains(ld, "/old") {
		t.Fatalf("LD_LIBRARY_PATH: %s", ld)
	}
	if dy != "DYLD_LIBRARY_PATH=/opt/tor" {
		t.Fatalf("DYLD_LIBRARY_PATH: %s", dy)
	}
}

func TestParseSocksListener(t *testing.T) {
	addr, err := parseSocksListener(`net/listeners/socks="127.0.0.1:9050"`)
	if err != nil || addr != "127.0.0.1:9050" {
		t.Fatalf("got %q %v", addr, err)
	}
	if _, err := parseSocksListener("nothing"); err == nil {
		t.Fatal("expected error")
	}
}
