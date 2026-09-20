// Package health keeps verified routes honest after startup: it probes each
// upstream, closes the gate the moment a route fails, re-verifies the egress
// IP before reopening it, and watches the browser process tree for any
// endpoint that is not the gate.
package health

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/routing/platform"
	"github.com/Nareik33L/tab-router/routing/provider"
)

// Kind classifies an Event.
type Kind int

const (
	RouteDown Kind = iota
	RouteRestored
	EgressChanged
	Leak
	BrowserExited
)

func (k Kind) String() string {
	switch k {
	case RouteDown:
		return "DOWN"
	case RouteRestored:
		return "READY"
	case EgressChanged:
		return "EGRESS CHANGED"
	case Leak:
		return "LEAK"
	case BrowserExited:
		return "BROWSER EXITED"
	}
	return "?"
}

// Event is a health transition worth telling the user about.
type Event struct {
	At      time.Time
	Subject *verify.Subject
	Kind    Kind
	Detail  string
}

// Options configures the monitor.
type Options struct {
	ProbeInterval   time.Duration
	IPCheckInterval time.Duration
	EchoURL         string
	Timeout         time.Duration
	OnEvent         func(Event)
}

// Monitor supervises a set of subjects.
type Monitor struct {
	opts     Options
	subjects []*verify.Subject
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	mu     sync.Mutex
	state  map[*verify.Subject]*State
	events []Event
}

// State is the latest observation for one subject.
type State struct {
	LastProbe   time.Time
	LastIPCheck time.Time
	LastError   string
	Leaks       []string
	Down        bool
	DownSince   time.Time
}

// New creates a monitor; call Start to run it.
func New(subjects []*verify.Subject, opts Options) *Monitor {
	if opts.ProbeInterval <= 0 {
		opts.ProbeInterval = 10 * time.Second
	}
	if opts.IPCheckInterval <= 0 {
		opts.IPCheckInterval = 60 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	m := &Monitor{opts: opts, subjects: subjects, state: map[*verify.Subject]*State{}}
	for _, s := range subjects {
		m.state[s] = &State{}
	}
	return m
}

// Start launches one supervision loop per subject.
func (m *Monitor) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	for _, s := range m.subjects {
		m.wg.Add(1)
		go m.loop(ctx, s)
	}
}

// Stop halts all loops.
func (m *Monitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

// StateOf returns a copy of the subject's state.
func (m *Monitor) StateOf(s *verify.Subject) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.state[s]; ok {
		cp := *st
		cp.Leaks = append([]string(nil), st.Leaks...)
		return cp
	}
	return State{}
}

// Events returns recorded transitions.
func (m *Monitor) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}

func (m *Monitor) emit(ev Event) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	if len(m.events) > 500 {
		m.events = m.events[len(m.events)-500:]
	}
	m.mu.Unlock()
	if m.opts.OnEvent != nil {
		m.opts.OnEvent(ev)
	}
}

func (m *Monitor) loop(ctx context.Context, s *verify.Subject) {
	defer m.wg.Done()
	probe := time.NewTicker(m.opts.ProbeInterval)
	defer probe.Stop()
	browserGone := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Browser.Exited():
			if !browserGone {
				browserGone = true
				s.Gate.Close()
				m.emit(Event{At: time.Now(), Subject: s, Kind: BrowserExited, Detail: "gate closed"})
			}
			// Keep probing the route so status stays truthful.
			select {
			case <-ctx.Done():
				return
			case <-probe.C:
			}
		case <-probe.C:
		}
		m.probe(ctx, s)
		if !browserGone {
			m.sampleLeaks(s)
		}
	}
}

func (m *Monitor) probe(ctx context.Context, s *verify.Subject) {
	st := m.state[s]
	pctx, cancel := context.WithTimeout(ctx, m.opts.Timeout)
	defer cancel()

	// Cheap reachability probe of the upstream itself.
	d := net.Dialer{Timeout: m.opts.Timeout}
	c, err := d.DialContext(pctx, "tcp", s.Route.Def().Address)
	if c != nil {
		c.Close()
	}
	now := time.Now()
	m.mu.Lock()
	st.LastProbe = now
	wasDown := st.Down
	m.mu.Unlock()

	if err != nil {
		m.markDown(s, st, "upstream unreachable: "+err.Error())
		return
	}

	// Full egress check: on recovery always, otherwise on the IP interval.
	needIP := wasDown || now.Sub(st.LastIPCheck) >= m.opts.IPCheckInterval
	if !needIP {
		return
	}
	if s.Route.Status() == provider.StatusDown {
		s.Route.SetStatus(provider.StatusVerifying)
	}
	ip, err := provider.PublicIP(pctx, s.Route.Dial, m.opts.EchoURL, m.opts.Timeout)
	m.mu.Lock()
	st.LastIPCheck = time.Now()
	m.mu.Unlock()
	if err != nil {
		m.markDown(s, st, "egress probe failed: "+err.Error())
		return
	}
	if s.RouteIP != nil && !ip.Equal(s.RouteIP) {
		m.markDown(s, st, fmt.Sprintf("egress changed %s -> %s", s.RouteIP, ip))
		m.emit(Event{At: time.Now(), Subject: s, Kind: EgressChanged, Detail: fmt.Sprintf("%s -> %s", s.RouteIP, ip)})
		return
	}
	if wasDown {
		m.mu.Lock()
		st.Down = false
		st.LastError = ""
		m.mu.Unlock()
		s.Route.SetStatus(provider.StatusReady)
		s.Gate.Open()
		m.emit(Event{At: time.Now(), Subject: s, Kind: RouteRestored, Detail: "egress " + ip.String() + " verified"})
	} else {
		s.Route.SetStatus(provider.StatusReady)
	}
}

func (m *Monitor) markDown(s *verify.Subject, st *State, reason string) {
	m.mu.Lock()
	first := !st.Down
	st.Down = true
	st.LastError = reason
	if first {
		st.DownSince = time.Now()
	}
	m.mu.Unlock()
	s.Route.SetStatus(provider.StatusDown)
	s.Gate.Close()
	if first {
		m.emit(Event{At: time.Now(), Subject: s, Kind: RouteDown, Detail: reason + "; traffic blocked"})
	}
}

func (m *Monitor) sampleLeaks(s *verify.Subject) {
	eps, err := s.Browser.Endpoints()
	if err != nil {
		return
	}
	var leaks []string
	for _, ep := range eps {
		if isLeak(ep, s.Gate.Addr()) {
			leaks = append(leaks, ep.String())
		}
	}
	m.mu.Lock()
	st := m.state[s]
	newLeaks := diff(leaks, st.Leaks)
	st.Leaks = leaks
	m.mu.Unlock()
	if len(newLeaks) > 0 {
		m.emit(Event{At: time.Now(), Subject: s, Kind: Leak, Detail: strings.Join(newLeaks, "; ")})
	}
}

func isLeak(ep platform.Endpoint, gate string) bool {
	if strings.HasPrefix(ep.Proto, "udp") {
		return true
	}
	if !ep.Remote.IsValid() || ep.Remote.Port() == 0 {
		return false
	}
	return ep.Remote.String() != gate
}

func diff(now, before []string) []string {
	seen := map[string]bool{}
	for _, b := range before {
		seen[b] = true
	}
	var out []string
	for _, n := range now {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}
