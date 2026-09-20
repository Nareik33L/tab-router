// Command tab-router-infra runs the local test network (IP echo + N upstream
// proxies with distinct source addresses) and writes a matching config.toml
// and routes.toml into --out so the real binary can be exercised:
//
//	tab-router-infra --out /tmp/tr &
//	tab-router --data-dir /tmp/tr --identities 2 --url http://ipecho.test:PORT/
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Nareik33L/tab-router/tests/infra"
)

func main() {
	out := flag.String("out", "", "directory to write config.toml and routes.toml into")
	proxies := flag.String("proxies", "socks5,socks5", "comma-separated upstream types")
	auth := flag.Bool("auth", false, "require credentials on the proxies")
	flag.Parse()

	in, err := infra.Start(infra.Options{Proxies: strings.Split(*proxies, ","), Auth: *auth})
	if err != nil {
		fmt.Fprintln(os.Stderr, "infra:", err)
		os.Exit(1)
	}
	defer in.Close()

	fmt.Printf("echo:      %s  (direct %s)\n", in.EchoURL(), in.EchoURLDirect())
	for i, p := range in.Proxies {
		fmt.Printf("route %03d: %s://%s  egress %s\n", i+1, p.Type, p.Addr, p.SourceIP)
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cfg := fmt.Sprintf(`identities = %d
headless = false

[verify]
ip_echo_url = %q
ipv6_echo_url = %q
host_echo_url = %q
compare_host_ip = true
timeout_seconds = 15

[health]
probe_interval_seconds = 3
ip_check_interval_seconds = 10
`, len(in.Proxies), in.EchoURL(), in.IPv6EchoURL(), in.EchoURLDirect())
		var routes strings.Builder
		for i, p := range in.Proxies {
			fmt.Fprintf(&routes, "[[route]]\nid = \"route-%03d\"\ntype = %q\naddress = %q\n", i+1, p.Type, p.Addr)
			if *auth {
				fmt.Fprintf(&routes, "username = %q\npassword = %q\n", p.Username, p.Password)
			}
			routes.WriteString("\n")
		}
		if err := os.WriteFile(filepath.Join(*out, "config.toml"), []byte(cfg), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(*out, "routes.toml"), []byte(routes.String()), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s/{config.toml,routes.toml}\n", *out)
	}
	fmt.Println("infra running; Ctrl-C to stop")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
