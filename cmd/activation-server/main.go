// Command activation-server issues licenses and serves the activation API.
// Decodo credentials are operator configuration (environment or per-license
// fields). They are not compiled into tab-router.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Nareik33L/tab-router/controller/activation/backend"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "issue" {
		os.Exit(issue(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "revoke" {
		os.Exit(revoke(os.Args[2:]))
	}
	os.Exit(serve(os.Args[1:]))
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	data := fs.String("data", "licenses.json", "license database")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	store, err := backend.OpenStore(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	proxy, err := proxyFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	srv := &backend.Server{Store: store, Proxy: proxy}
	fmt.Fprintf(os.Stderr, "activation server listening on %s\n", *listen)
	if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func issue(args []string) int {
	fs := flag.NewFlagSet("issue", flag.ContinueOnError)
	data := fs.String("data", "licenses.json", "license database")
	max := fs.Int("max-installations", 1, "installations allowed for this key")
	days := fs.Int("days", 365, "days until the key expires")
	user := fs.String("proxy-user", "", "limited Decodo username for this key (default: server env)")
	pass := fs.String("proxy-pass", "", "limited Decodo password for this key (default: server env)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	store, err := backend.OpenStore(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	key, err := store.Issue(*max, time.Now().Add(time.Duration(*days)*24*time.Hour), *user, *pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println(key)
	return 0
}

func revoke(args []string) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	data := fs.String("data", "licenses.json", "license database")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: activation-server revoke [-data file] KEY-OR-ID")
		return 2
	}
	store, err := backend.OpenStore(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := store.Revoke(fs.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("revoked")
	return 0
}

func proxyFromEnv() (backend.ProxyAccess, error) {
	port := 7000
	if v := os.Getenv("DECODO_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return backend.ProxyAccess{}, fmt.Errorf("DECODO_PORT: %w", err)
		}
		port = n
	}
	minutes := 1440
	if v := os.Getenv("DECODO_SESSION_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return backend.ProxyAccess{}, fmt.Errorf("DECODO_SESSION_MINUTES: %w", err)
		}
		minutes = n
	}
	host := os.Getenv("DECODO_HOST")
	if host == "" {
		host = "gate.decodo.com"
	}
	return backend.ProxyAccess{
		Host:           host,
		Port:           port,
		Username:       os.Getenv("DECODO_USERNAME"),
		Password:       os.Getenv("DECODO_PASSWORD"),
		Country:        os.Getenv("DECODO_COUNTRY"),
		SessionMinutes: minutes,
	}, nil
}
