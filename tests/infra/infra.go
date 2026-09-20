// Package infra provides the local test network Tab Router's isolation
// tests run against: an IP-echo HTTP server and N upstream proxies, each
// dialing out from a distinct loopback source address so the echo server
// sees a different "public IP" per route.
//
// It requires that 127.0.0.2..127.0.0.N are usable loopback addresses. This
// is true on Linux by default; on macOS run
//
//	sudo ifconfig lo0 alias 127.0.0.2 up   (and so on)
//
// before running the suite.
package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/routing/httpproxy"
	"github.com/Nareik33L/tab-router/routing/socks5"
)

// EchoHost is the hostname routes must resolve for the echo server.
const EchoHost = "ipecho.test"

// UnresolvableHost is never in any proxy's hosts map; requests to it must
// fail, which the IPv6 check treats as "blocked".
const UnresolvableHost = "ipecho6.test"

// Proxy is one emulated upstream.
type Proxy struct {
	Type     string // "socks5" or "http"
	Addr     string // listen address host:port
	SourceIP net.IP // egress address the echo server will see
	Username string
	Password string

	mu      sync.Mutex
	lookups []string // hostnames this proxy was asked to resolve
	socks   *socks5.Server
	httpSrv *httpproxy.Server
	hosts   map[string]string
	refuse  bool
}

// Lookups returns hostnames the proxy resolved (its "DNS log").
func (p *Proxy) Lookups() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lookups...)
}

// SetRefuse makes the proxy reject all new connections (simulated outage).
func (p *Proxy) SetRefuse(v bool) {
	p.mu.Lock()
	p.refuse = v
	p.mu.Unlock()
	if v {
		if p.socks != nil {
			p.socks.CloseConnections()
		}
	}
}

func (p *Proxy) dial(ctx context.Context, hostport string) (net.Conn, error) {
	p.mu.Lock()
	refuse := p.refuse
	p.mu.Unlock()
	if refuse {
		return nil, fmt.Errorf("upstream unavailable")
	}
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	target := host
	if net.ParseIP(host) == nil {
		p.mu.Lock()
		p.lookups = append(p.lookups, strings.ToLower(host))
		ip, ok := p.hosts[strings.ToLower(host)]
		p.mu.Unlock()
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		target = ip
	}
	d := net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: p.SourceIP}}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(target, portStr))
}

// Close stops the proxy.
func (p *Proxy) Close() {
	if p.socks != nil {
		p.socks.Close()
	}
	if p.httpSrv != nil {
		p.httpSrv.Close()
	}
}

// Options configures Start.
type Options struct {
	// Proxies lists the type of each upstream to create ("socks5"/"http").
	Proxies []string
	// Auth, if true, requires user/pass "user-N"/"pass-N" on proxy N.
	Auth bool
}

// Infra is a running test network.
type Infra struct {
	Echo    *http.Server
	EchoLn  net.Listener
	Proxies []*Proxy
	hits    []Hit
	mu      sync.Mutex
}

// Hit records a request seen by the echo server.
type Hit struct {
	At       time.Time
	ClientIP string
	Path     string
	Host     string
}

// EchoPort returns the echo server's port.
func (in *Infra) EchoPort() int { return in.EchoLn.Addr().(*net.TCPAddr).Port }

// EchoURL is the echo endpoint by hostname (resolution must happen at the
// proxy).
func (in *Infra) EchoURL() string {
	return fmt.Sprintf("http://%s:%d/", EchoHost, in.EchoPort())
}

// EchoURLDirect is the echo endpoint by IP, for the controller's host-IP probe.
func (in *Infra) EchoURLDirect() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", in.EchoPort())
}

// IPv6EchoURL points at a hostname no proxy can resolve, so the IPv6 check
// observes "blocked".
func (in *Infra) IPv6EchoURL() string {
	return fmt.Sprintf("http://%s:%d/", UnresolvableHost, in.EchoPort())
}

// Hits returns requests seen by the echo server.
func (in *Infra) Hits() []Hit {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]Hit(nil), in.hits...)
}

// Start brings up the echo server and proxies.
func Start(opts Options) (*Infra, error) {
	in := &Infra{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	in.EchoLn = ln
	in.Echo = &http.Server{Handler: in.handler(), ReadHeaderTimeout: 5 * time.Second}
	go in.Echo.Serve(ln)

	hosts := map[string]string{EchoHost: "127.0.0.1"}
	for i, typ := range opts.Proxies {
		src := net.IPv4(127, 0, 0, byte(2+i))
		p := &Proxy{Type: typ, SourceIP: src, hosts: hosts}
		if opts.Auth {
			p.Username = fmt.Sprintf("user-%d", i+1)
			p.Password = fmt.Sprintf("pass-%d", i+1)
		}
		// Prove the source address is bindable before handing it out.
		probe, err := net.Listen("tcp", net.JoinHostPort(src.String(), "0"))
		if err != nil {
			in.Close()
			return nil, fmt.Errorf("loopback alias %s unavailable: %w (see tests/infra docs)", src, err)
		}
		probe.Close()
		switch typ {
		case "socks5":
			p.socks = &socks5.Server{Dial: p.dial}
			if opts.Auth {
				u, pw := p.Username, p.Password
				p.socks.Authenticate = func(user, pass string) bool { return user == u && pass == pw }
			}
			addr, err := p.socks.Listen("127.0.0.1:0")
			if err != nil {
				in.Close()
				return nil, err
			}
			p.Addr = addr.String()
		case "http":
			p.httpSrv = &httpproxy.Server{Dial: p.dial}
			if opts.Auth {
				u, pw := p.Username, p.Password
				p.httpSrv.Authenticate = func(user, pass string) bool { return user == u && pass == pw }
			}
			addr, err := p.httpSrv.Listen("127.0.0.1:0")
			if err != nil {
				in.Close()
				return nil, err
			}
			p.Addr = addr.String()
		default:
			in.Close()
			return nil, fmt.Errorf("unknown proxy type %q", typ)
		}
		in.Proxies = append(in.Proxies, p)
	}
	return in, nil
}

// Close shuts everything down.
func (in *Infra) Close() {
	for _, p := range in.Proxies {
		p.Close()
	}
	if in.Echo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = in.Echo.Shutdown(ctx)
		cancel()
	}
}

func (in *Infra) handler() http.Handler {
	mux := http.NewServeMux()
	record := func(r *http.Request) string {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		in.mu.Lock()
		in.hits = append(in.hits, Hit{At: time.Now(), ClientIP: ip, Path: r.URL.Path, Host: r.Host})
		in.mu.Unlock()
		return ip
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ip := record(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"ip": ip})
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		ip := record(r)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, ip)
	})
	// /redirect/N hops through N 302s before landing on /.
	mux.HandleFunc("/redirect/", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/redirect/"))
		if n <= 1 {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/redirect/%d", n-1), http.StatusFound)
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		ip := record(r)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="probe.txt"`)
		fmt.Fprintf(w, "client=%s\n", ip)
	})
	mux.HandleFunc("/store", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>store</title><body>store</body>`)
	})
	return mux
}
