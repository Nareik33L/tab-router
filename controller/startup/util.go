package startup

import (
	"net"
	"os"

	"github.com/Nareik33L/tab-router/controller/verify"
)

func pid() int { return os.Getpid() }

func parseIP(s string) net.IP { return net.ParseIP(s) }

func uniqueChecks(results []verify.Check) int {
	seen := map[string]bool{}
	for _, c := range results {
		seen[c.ID] = true
	}
	return len(seen)
}
