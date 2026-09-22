package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/controller/window"
	"github.com/Nareik33L/tab-router/ipc"
)

// Status is the JSON document returned by the status IPC method.
type Status struct {
	Version    string           `json:"version"`
	PID        int              `json:"pid"`
	StartedAt  time.Time        `json:"started_at"`
	Chromium   string           `json:"chromium"`
	Source     string           `json:"chromium_source"`
	HostIPv4   string           `json:"host_ipv4,omitempty"`
	HostIPv6   string           `json:"host_ipv6,omitempty"`
	Identities []IdentityStatus `json:"identities"`
	Events     []string         `json:"recent_events"`
	Windows    *window.Info     `json:"windows,omitempty"`
}

// IdentityStatus is one identity's live state.
type IdentityStatus struct {
	Index        int                  `json:"index"`
	ID           string               `json:"id"`
	Route        string               `json:"route"`
	RouteType    string               `json:"route_type"`
	RouteStatus  string               `json:"route_status"`
	GateOpen     bool                 `json:"gate_open"`
	GateAddr     string               `json:"gate_addr"`
	BrowserPID   int                  `json:"browser_pid"`
	BrowserAlive bool                 `json:"browser_alive"`
	EgressIP     string               `json:"egress_ip,omitempty"`
	CurrentURL   string               `json:"current_url,omitempty"`
	Verified     bool                 `json:"verified"`
	Checks       []CheckStatus        `json:"checks"`
	Environment  identity.Environment `json:"environment"`
	Profile      string               `json:"profile"`
	Health       HealthStatus         `json:"health"`
}

// CheckStatus is a verification result.
type CheckStatus struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// HealthStatus is the monitor's view.
type HealthStatus struct {
	LastProbe time.Time `json:"last_probe"`
	Down      bool      `json:"down"`
	DownSince time.Time `json:"down_since,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	Leaks     []string  `json:"leaks,omitempty"`
}

// Status builds the current report.
func (s *Session) Status() Status {
	st := Status{Version: Version, PID: pid(), StartedAt: s.Started, Chromium: s.ChromeVer, Source: s.Chromium.Source, HostIPv4: s.hostIPv4, HostIPv6: s.hostIPv6}
	if s.windows != nil {
		info := s.windows.Info()
		st.Windows = &info
	}
	for _, sub := range s.subjects {
		is := IdentityStatus{
			Index:       sub.Identity.Index,
			ID:          sub.Identity.ID,
			Route:       sub.Route.ID(),
			RouteType:   sub.Route.Def().Type,
			RouteStatus: sub.Route.Status().String(),
			GateOpen:    sub.Gate.IsOpen(),
			GateAddr:    sub.Gate.Addr(),
			Verified:    !sub.Failed() && len(sub.Results) > 0,
			Environment: sub.Identity.Environment,
			Profile:     s.ProfileRelPath(sub),
		}
		if sub.BrowserIP != nil {
			is.EgressIP = sub.BrowserIP.String()
		}
		if sub.Browser != nil {
			is.BrowserPID = sub.Browser.PID()
			is.BrowserAlive = sub.Browser.Alive()
		}
		s.mu.Lock()
		is.CurrentURL = s.finalURL[sub]
		s.mu.Unlock()
		for _, c := range sub.Results {
			is.Checks = append(is.Checks, CheckStatus{ID: c.ID, Name: c.Name, Passed: c.Passed, Detail: c.Detail})
		}
		if s.monitor != nil {
			h := s.monitor.StateOf(sub)
			is.Health = HealthStatus{LastProbe: h.LastProbe, Down: h.Down, DownSince: h.DownSince, LastError: h.LastError, Leaks: h.Leaks}
		}
		st.Identities = append(st.Identities, is)
	}
	if s.monitor != nil {
		evs := s.monitor.Events()
		if len(evs) > 20 {
			evs = evs[len(evs)-20:]
		}
		for _, ev := range evs {
			st.Events = append(st.Events, fmt.Sprintf("%s %s %s: %s", ev.At.Format(time.RFC3339), ev.Subject.Label(), ev.Kind, ev.Detail))
		}
	}
	return st
}

// IPCHandlers exposes the session over the control channel. requestStop is
// invoked for the shutdown method.
func (s *Session) IPCHandlers(requestStop func()) map[string]ipc.Handler {
	return map[string]ipc.Handler{
		"status": func(ctx context.Context, _ json.RawMessage) (any, error) { return s.Status(), nil },
		"identities.list": func(ctx context.Context, _ json.RawMessage) (any, error) {
			out := make([]identity.Identity, 0, len(s.subjects))
			for _, sub := range s.subjects {
				out = append(out, sub.Identity)
			}
			return out, nil
		},
		"routes.list": func(ctx context.Context, _ json.RawMessage) (any, error) {
			type r struct {
				ID, Type, Status, Redacted string
				Slot                       int
			}
			out := make([]r, 0, len(s.subjects))
			for _, sub := range s.subjects {
				out = append(out, r{ID: sub.Route.ID(), Type: sub.Route.Def().Type, Status: sub.Route.Status().String(), Redacted: sub.Route.Def().Redacted(), Slot: sub.Identity.RouteSlot})
			}
			return out, nil
		},
		"route.stop": func(ctx context.Context, p json.RawMessage) (any, error) {
			var a struct{ Slot int }
			if err := json.Unmarshal(p, &a); err != nil {
				return nil, err
			}
			return nil, s.SetRouteEnabled(ctx, a.Slot, false)
		},
		"route.start": func(ctx context.Context, p json.RawMessage) (any, error) {
			var a struct{ Slot int }
			if err := json.Unmarshal(p, &a); err != nil {
				return nil, err
			}
			return nil, s.SetRouteEnabled(ctx, a.Slot, true)
		},
		"identity.open_url": func(ctx context.Context, p json.RawMessage) (any, error) {
			var a struct {
				Index int
				URL   string
			}
			if err := json.Unmarshal(p, &a); err != nil {
				return nil, err
			}
			return nil, s.OpenURL(ctx, a.Index, a.URL)
		},
		"diagnostics.run": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.Diagnostics(ctx)
		},
		"shutdown": func(ctx context.Context, _ json.RawMessage) (any, error) {
			go requestStop()
			return "stopping", nil
		},
		"windows.tile": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.tileWindows(ctx)
		},
		"windows.restore": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return nil, s.restoreWindows(ctx)
		},
		"windows.manual": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.windowsMode("manual")
		},
		"windows.auto": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.windowsMode("auto")
		},
		"windows.focus_next": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.focusWindows(ctx, 1)
		},
		"windows.focus_prev": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.focusWindows(ctx, -1)
		},
		"windows.displays": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.listDisplays()
		},
		"windows.display": func(ctx context.Context, p json.RawMessage) (any, error) {
			var a struct {
				Display string `json:"display"`
			}
			if err := json.Unmarshal(p, &a); err != nil {
				return nil, err
			}
			return s.setDisplay(ctx, a.Display)
		},
	}
}

// Diagnostics re-runs the browser-side verification suite against the live
// session and returns per-identity results. It deliberately induces a route
// failure (V7) and confirms blocking.
func (s *Session) Diagnostics(ctx context.Context) (Status, error) {
	o := s.verifyOptions()
	for _, sub := range s.subjects {
		if sub.Browser == nil || !sub.Browser.Alive() {
			return Status{}, fmt.Errorf("%s browser is not running", sub.Label())
		}
		sub.Results = nil
		if err := verify.Begin(ctx, sub); err != nil {
			return Status{}, err
		}
	}
	defer func() {
		for _, sub := range s.subjects {
			verify.Finish(ctx, sub)
		}
	}()
	for _, sub := range s.subjects {
		verify.V1RouteUp(ctx, sub, o)
		verify.V2BrowserEgress(ctx, sub, o)
	}
	verify.V3Separation(s.subjects, o)
	for i, sub := range s.subjects {
		verify.V4DNSDelegation(sub, o)
		verify.V5IPv6(ctx, sub, o)
		if len(s.subjects) > 1 {
			verify.V6StorageIsolation(ctx, sub, s.subjects[(i+1)%len(s.subjects)], o)
		}
		verify.V7FailClosed(ctx, sub, o)
		verify.V8NoDirect(sub, o)
	}
	return s.Status(), nil
}

func (s *Session) verifyOptions() verify.Options {
	o := verify.Options{
		EchoURL:       s.Cfg.Verify.IPEchoURL,
		IPv6EchoURL:   s.Cfg.Verify.IPv6EchoURL,
		StorageOrigin: s.Cfg.Verify.StorageOrigin,
		CompareHostIP: s.Cfg.Verify.CompareHostIP,
		Timeout:       time.Duration(s.Cfg.Verify.TimeoutSeconds) * time.Second,
	}
	if !s.Cfg.IPv6 {
		o.IPv6EchoURL = ""
	}
	if s.hostIPv4 != "" {
		o.HostIPv4 = parseIP(s.hostIPv4)
	}
	if s.hostIPv6 != "" {
		o.HostIPv6 = parseIP(s.hostIPv6)
	}
	return o
}
