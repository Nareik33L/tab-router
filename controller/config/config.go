// Package config loads config.toml (non-secret settings) and routes.toml
// (upstream definitions, which may contain credentials) and applies CLI
// overrides.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/routing/platform"
	"github.com/Nareik33L/tab-router/routing/provider"
)

// MaxIdentities is the hard cap for the 2-identity milestone. It is lifted
// in M9 once the isolation suite passes at scale.
const MaxIdentities = 2

// Config is the effective, merged configuration.
type Config struct {
	Identities           int    `toml:"identities"`
	StartupURL           string `toml:"startup_url"`
	PersistentIdentities bool   `toml:"persistent_identities"`
	FailClosed           bool   `toml:"fail_closed"`
	IPv6                 bool   `toml:"ipv6"`
	Headless             bool   `toml:"headless"`

	Verify     VerifyConfig     `toml:"verify"`
	Health     HealthConfig     `toml:"health"`
	Routing    RoutingConfig    `toml:"routing"`
	Activation ActivationConfig `toml:"activation"`

	// Environment holds defaults applied to identities when they are first
	// created; Identity entries override them per index. Neither affects an
	// identity that already exists (its identity.json is authoritative).
	Environment EnvironmentConfig   `toml:"environment"`
	Identity    []IdentityEnvConfig `toml:"identity"`

	// Paths are resolved, never read from the file.
	DataDir    string `toml:"-"`
	ConfigPath string `toml:"-"`
	RoutesPath string `toml:"-"`
	Fresh      bool   `toml:"-"`
}

// VerifyConfig controls startup verification.
type VerifyConfig struct {
	IPEchoURL   string `toml:"ip_echo_url"`
	IPv6EchoURL string `toml:"ipv6_echo_url"`
	// HostEchoURL is fetched once over the host connection to learn the
	// host's own IP for the "≠ host" comparison. Defaults to ip_echo_url.
	HostEchoURL   string `toml:"host_echo_url"`
	HostEchoURLv6 string `toml:"host_echo_url_v6"`
	CompareHostIP bool   `toml:"compare_host_ip"`
	// StorageOrigin is the origin used for the cookie isolation check; it
	// defaults to the echo URL's origin.
	StorageOrigin string `toml:"storage_origin"`
	// TimeoutSeconds bounds each individual check.
	TimeoutSeconds int `toml:"timeout_seconds"`
}

// HealthConfig controls the health monitor.
type HealthConfig struct {
	ProbeIntervalSeconds   int `toml:"probe_interval_seconds"`
	IPCheckIntervalSeconds int `toml:"ip_check_interval_seconds"`
}

// RoutingConfig carries optional Decodo gateway settings. Startup always
// uses Decodo. Host, port, country, and session length are optional.
// An unknown provider name is rejected. Tor is not selected.
type RoutingConfig struct {
	Provider       string `toml:"provider"`
	Country        string `toml:"country"`
	SessionMinutes int    `toml:"session_minutes"`
	Host           string `toml:"host"`
	Port           int    `toml:"port"`
}

// ActivationConfig is the non-secret address of the activation backend.
type ActivationConfig struct {
	Server string `toml:"server"`
}

// DecodoSelected reports whether config.toml names the Decodo provider.
// Startup uses Decodo either way; this only reflects the written value.
func (c Config) DecodoSelected() bool {
	return strings.EqualFold(strings.TrimSpace(c.Routing.Provider), "decodo")
}

// EnvironmentConfig are creation-time defaults for identity environments.
// Empty strings mean "detect from the host once, then pin".
type EnvironmentConfig struct {
	Locale            string  `toml:"locale"`
	Timezone          string  `toml:"timezone"`
	Window            string  `toml:"window"` // "1280x860"
	DeviceScaleFactor float64 `toml:"device_scale_factor"`
	ColorScheme       string  `toml:"color_scheme"`
}

// IdentityEnvConfig overrides environment defaults for one identity index.
type IdentityEnvConfig struct {
	Index int `toml:"index"`
	EnvironmentConfig
}

func parseWindow(s string) (int, int, error) {
	if s == "" {
		return 0, 0, nil
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.ToLower(s), "%dx%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("environment.window %q must look like 1280x860", s)
	}
	return w, h, nil
}

// EnvironmentFor returns the creation-time environment for identity index,
// merging global defaults with any per-identity override.
func (c *Config) EnvironmentFor(index int) (identity.Environment, error) {
	d := identity.EnvironmentDefaults{
		Locale:            c.Environment.Locale,
		Timezone:          c.Environment.Timezone,
		DeviceScaleFactor: c.Environment.DeviceScaleFactor,
		ColorScheme:       c.Environment.ColorScheme,
	}
	var err error
	if d.WindowWidth, d.WindowHeight, err = parseWindow(c.Environment.Window); err != nil {
		return identity.Environment{}, err
	}
	for _, o := range c.Identity {
		if o.Index != index {
			continue
		}
		if o.Locale != "" {
			d.Locale = o.Locale
		}
		if o.Timezone != "" {
			d.Timezone = o.Timezone
		}
		if o.DeviceScaleFactor != 0 {
			d.DeviceScaleFactor = o.DeviceScaleFactor
		}
		if o.ColorScheme != "" {
			d.ColorScheme = o.ColorScheme
		}
		if o.Window != "" {
			if d.WindowWidth, d.WindowHeight, err = parseWindow(o.Window); err != nil {
				return identity.Environment{}, err
			}
		}
	}
	env := identity.NewEnvironment(index, d)
	if err := env.Validate(); err != nil {
		return identity.Environment{}, fmt.Errorf("identity %03d: %w", index, err)
	}
	return env, nil
}

// Routes is the parsed routes.toml.
type Routes struct {
	Route []provider.RouteDef `toml:"route"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Identities:           2,
		PersistentIdentities: true,
		FailClosed:           true,
		IPv6:                 true,
		Verify: VerifyConfig{
			IPEchoURL:      "https://api.ipify.org?format=json",
			IPv6EchoURL:    "https://api6.ipify.org?format=json",
			CompareHostIP:  true,
			TimeoutSeconds: 20,
		},
		Health: HealthConfig{ProbeIntervalSeconds: 10, IPCheckIntervalSeconds: 60},
	}
}

// Overrides are CLI values; zero values mean "not set".
type Overrides struct {
	Identities int
	URL        string
	URLSet     bool
	Fresh      bool
	Headless   bool
	DataDir    string
	ConfigPath string
	RoutesPath string
}

// Load merges defaults, config.toml (if present) and overrides.
func Load(o Overrides) (Config, error) {
	cfg := Defaults()
	dataDir := o.DataDir
	if dataDir == "" {
		dataDir = os.Getenv("TAB_ROUTER_DATA_DIR")
	}
	if dataDir == "" {
		d, err := platform.Current().DefaultDataDir()
		if err != nil {
			return cfg, err
		}
		dataDir = d
	}
	cfg.DataDir = dataDir
	cfg.ConfigPath = o.ConfigPath
	if cfg.ConfigPath == "" {
		cfg.ConfigPath = filepath.Join(dataDir, "config.toml")
	}
	cfg.RoutesPath = o.RoutesPath
	if cfg.RoutesPath == "" {
		cfg.RoutesPath = filepath.Join(dataDir, "routes.toml")
	}

	if b, err := os.ReadFile(cfg.ConfigPath); err == nil {
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", cfg.ConfigPath, err)
		}
	} else if !os.IsNotExist(err) || o.ConfigPath != "" {
		if !os.IsNotExist(err) {
			return cfg, err
		}
		return cfg, fmt.Errorf("config file %s not found", o.ConfigPath)
	}

	if o.Identities != 0 {
		cfg.Identities = o.Identities
	}
	if o.URLSet {
		cfg.StartupURL = o.URL
	}
	if o.Headless {
		cfg.Headless = true
	}
	cfg.Fresh = o.Fresh
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func applyEnv(cfg *Config) error {
	if v := strings.TrimSpace(os.Getenv("TAB_ROUTER_PROVIDER")); v != "" {
		cfg.Routing.Provider = v
	}
	if v := strings.TrimSpace(os.Getenv("TAB_ROUTER_ACTIVATION_SERVER")); v != "" {
		cfg.Activation.Server = v
	}
	if cfg.Activation.Server != "" {
		if err := ValidateURL(cfg.Activation.Server); err != nil {
			return fmt.Errorf("activation server: %w", err)
		}
	}
	return nil
}

// Validate checks the merged configuration.
func (c *Config) Validate() error {
	if c.Identities < 1 {
		return errors.New("identities must be at least 1")
	}
	if c.Identities > MaxIdentities {
		return fmt.Errorf("identities=%d exceeds the current cap of %d (the 2-identity milestone must pass its isolation suite before the cap is lifted; see docs/ENGINEERING_PLAN.md M9)", c.Identities, MaxIdentities)
	}
	if !c.FailClosed {
		return errors.New("fail_closed=false is not supported in v0.1")
	}
	if c.StartupURL != "" {
		if err := ValidateURL(c.StartupURL); err != nil {
			return err
		}
	}
	if c.Verify.IPEchoURL == "" {
		return errors.New("verify.ip_echo_url is required")
	}
	if c.Verify.TimeoutSeconds <= 0 {
		c.Verify.TimeoutSeconds = 20
	}
	if c.Verify.HostEchoURL == "" {
		c.Verify.HostEchoURL = c.Verify.IPEchoURL
	}
	if c.Verify.HostEchoURLv6 == "" {
		c.Verify.HostEchoURLv6 = c.Verify.IPv6EchoURL
	}
	if c.Health.ProbeIntervalSeconds <= 0 {
		c.Health.ProbeIntervalSeconds = 10
	}
	if c.Health.IPCheckIntervalSeconds <= 0 {
		c.Health.IPCheckIntervalSeconds = 60
	}
	return nil
}

// ValidateURL accepts absolute http/https URLs only. The URL is never
// rewritten.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid URL %q: missing host", raw)
	}
	return nil
}

// ErrNoRoutes is returned when routes.toml does not exist.
var ErrNoRoutes = errors.New("routes file not found")

// LoadRoutes reads routes.toml, refusing files readable by other users.
func LoadRoutes(path string) ([]provider.RouteDef, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoRoutes, path)
		}
		return nil, err
	}
	if err := platform.Current().CheckFilePrivate(path); err != nil {
		return nil, fmt.Errorf("refusing to read route credentials: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Routes
	if err := toml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i := range r.Route {
		if err := r.Route[i].Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if seen[r.Route[i].ID] {
			return nil, fmt.Errorf("%s: duplicate route id %q", path, r.Route[i].ID)
		}
		seen[r.Route[i].ID] = true
	}
	return r.Route, nil
}

// ExampleRoutes is the placeholder file shipped as routes.example.toml for
// power users who supply their own upstreams instead of automatic exits.
const ExampleRoutes = `# Optional power-user override. Normal startup does not need this file.
# Keep this file private (chmod 600). Never commit it.

[[route]]
id = "route-001"
type = "socks5"                      # socks5 | http | wireguard
address = "proxy-a.example.net:1080"
username = "alice"
password_env = "TR_ROUTE_001_PASSWORD"   # or: password = "..."

[[route]]
id = "route-002"
type = "http"
address = "proxy-b.example.net:3128"
`

// MissingRoutesMessage explains how to create routes.toml when --routes is set.
func MissingRoutesMessage(path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "No route definitions found at %s\n\n", path)
	b.WriteString("Normal startup does not need this file. Run:\n\n")
	b.WriteString("  tab-router --identities 2 --url https://example.com\n\n")
	b.WriteString("To supply your own upstreams instead, create the file with owner-only\n")
	b.WriteString("permissions, for example:\n\n")
	b.WriteString(ExampleRoutes)
	return b.String()
}
