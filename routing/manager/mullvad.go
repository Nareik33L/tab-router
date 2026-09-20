package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Nareik33L/tab-router/routing/platform"
	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/wireguard"
)

const (
	mullvadRelaysURL = "https://api.mullvad.net/www/relays/wireguard"
	mullvadAuthURL   = "https://api.mullvad.net/auth/v1/token"
	mullvadDevices   = "https://api.mullvad.net/accounts/v1/devices"
	mullvadDNS       = "10.64.0.1"
	mullvadPort      = "51820"
)

// File is the persisted one-time provider configuration.
type File struct {
	Type    string   `toml:"type"`
	Account string   `toml:"account"`
	Device  []Device `toml:"device"`
}

// Device is one Mullvad WireGuard device (one per identity slot).
type Device struct {
	Slot       int    `toml:"slot"`
	ID         string `toml:"id"`
	Name       string `toml:"name"`
	PrivateKey string `toml:"private_key"`
	IPv4       string `toml:"ipv4"`
	IPv6       string `toml:"ipv6"`
}

// Path is <data-dir>/provider.toml.
func Path(dataDir string) string { return filepath.Join(dataDir, "provider.toml") }

// LoadFile reads provider.toml, refusing world-readable files.
func LoadFile(path string) (File, error) {
	var f File
	if _, err := os.Stat(path); err != nil {
		return f, err
	}
	if err := platform.Current().CheckFilePrivate(path); err != nil {
		return f, fmt.Errorf("refusing to read provider credentials: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := toml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// SaveFile writes provider.toml with owner-only permissions.
func SaveFile(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := platform.Current().SecureFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// MissingProviderMessage is printed when neither a provider nor routes.toml
// is configured.
func MissingProviderMessage(dataDir string) string {
	var b strings.Builder
	b.WriteString("No network provider configured.\n\n")
	b.WriteString("Tab Router creates one independent route per identity. It cannot\n")
	b.WriteString("invent public IP addresses, so it needs a network-exit provider.\n")
	b.WriteString("The first provider is Mullvad, via a userspace WireGuard tunnel\n")
	b.WriteString("(no administrator rights, no host routing changes).\n\n")
	b.WriteString("One-time setup — paste your Mullvad account number:\n\n")
	b.WriteString("  tab-router provider login\n\n")
	fmt.Fprintf(&b, "It is stored owner-only in %s\n", Path(dataDir))
	b.WriteString("and is never sent to Chromium or printed in logs.\n\n")
	b.WriteString("A Mullvad account is required because two identities need two\n")
	b.WriteString("independent public egress IPs. After login:\n\n")
	b.WriteString("  tab-router --identities 2 --url https://example.com\n")
	return b.String()
}

// Mullvad provisions one userspace WireGuard tunnel per identity, each
// terminating on a different Mullvad relay so egress IPs differ.
type Mullvad struct {
	path string
	file File
	http *http.Client

	// Overridable in tests. Empty means the production Mullvad API.
	authURL    string
	devicesURL string
	relaysURL  string

	mu     sync.Mutex
	relays []Relay
	used   map[string]bool // ipv4 already assigned this session
}

// Relay is one Mullvad WireGuard server.
type Relay struct {
	Hostname   string `json:"hostname"`
	Country    string `json:"country_code"`
	City       string `json:"city_code"`
	Active     bool   `json:"active"`
	IPv4       string `json:"ipv4_addr_in"`
	PubKey     string `json:"pubkey"`
	SOCKSName  string `json:"socks_name"`
}

// OpenMullvad loads the saved account and ensures one device per slot.
func OpenMullvad(ctx context.Context, path string, n int) (*Mullvad, error) {
	f, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(f.Type, "mullvad") {
		return nil, fmt.Errorf("provider type %q is not mullvad", f.Type)
	}
	if strings.TrimSpace(f.Account) == "" {
		return nil, fmt.Errorf("provider.toml: account is empty")
	}
	m := &Mullvad{path: path, file: f, http: &http.Client{Timeout: 30 * time.Second}, used: map[string]bool{}}
	if err := m.ensureDevices(ctx, n); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Mullvad) Name() string { return "mullvad" }

func (m *Mullvad) Close() error { return nil }

func (m *Mullvad) Provision(ctx context.Context, n int) ([]provider.Route, error) {
	relays, err := m.pick(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Route, 0, n)
	for i := 0; i < n; i++ {
		dev, ok := m.device(i + 1)
		if !ok {
			return nil, fmt.Errorf("mullvad: no device for slot %d", i+1)
		}
		r, err := provider.New(m.def(i+1, dev, relays[i]))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *Mullvad) Reestablish(ctx context.Context, slot int, failed provider.Route) (provider.Route, error) {
	if failed != nil {
		if ip := strings.Split(failed.Def().Address, ":")[0]; ip != "" {
			m.mu.Lock()
			m.used[ip] = true
			m.mu.Unlock()
		}
	}
	relays, err := m.pick(ctx, 1)
	if err != nil {
		return nil, err
	}
	dev, ok := m.device(slot)
	if !ok {
		return nil, fmt.Errorf("mullvad: no device for slot %d", slot)
	}
	return provider.New(m.def(slot, dev, relays[0]))
}

func (m *Mullvad) def(slot int, dev Device, rel Relay) provider.RouteDef {
	return provider.RouteDef{
		ID:            fmt.Sprintf("route-%03d", slot),
		Type:          "wireguard",
		Address:       netJoin(rel.IPv4, mullvadPort),
		PrivateKey:    dev.PrivateKey,
		PeerPublicKey: rel.PubKey,
		LocalAddress:  stripCIDR(dev.IPv4),
		LocalAddress6: stripCIDR(dev.IPv6),
		DNS:           mullvadDNS,
	}
}

func netJoin(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func (m *Mullvad) device(slot int) (Device, bool) {
	for _, d := range m.file.Device {
		if d.Slot == slot {
			return d, true
		}
	}
	return Device{}, false
}

func (m *Mullvad) ensureDevices(ctx context.Context, n int) error {
	need := false
	for slot := 1; slot <= n; slot++ {
		if _, ok := m.device(slot); !ok {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	token, err := m.token(ctx)
	if err != nil {
		return err
	}
	changed := false
	for slot := 1; slot <= n; slot++ {
		if _, ok := m.device(slot); ok {
			continue
		}
		priv, err := wireguard.GeneratePrivateKey()
		if err != nil {
			return err
		}
		dev, err := m.createDevice(ctx, token, priv.Public().Base64())
		if err != nil {
			return fmt.Errorf("mullvad: create device for slot %d: %w", slot, err)
		}
		m.file.Device = append(m.file.Device, Device{
			Slot:       slot,
			ID:         dev.ID,
			Name:       dev.Name,
			PrivateKey: priv.Base64(),
			IPv4:       stripCIDR(dev.IPv4),
			IPv6:       stripCIDR(dev.IPv6),
		})
		changed = true
	}
	if changed {
		if err := SaveFile(m.path, m.file); err != nil {
			return err
		}
	}
	return nil
}

type apiDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	IPv4 string `json:"ipv4_address"`
	IPv6 string `json:"ipv6_address"`
}

func (m *Mullvad) endpoint(kind string) string {
	switch kind {
	case "auth":
		if m.authURL != "" {
			return m.authURL
		}
		return mullvadAuthURL
	case "devices":
		if m.devicesURL != "" {
			return m.devicesURL
		}
		return mullvadDevices
	default:
		if m.relaysURL != "" {
			return m.relaysURL
		}
		return mullvadRelaysURL
	}
}

func (m *Mullvad) token(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{"account_number": strings.TrimSpace(m.file.Account)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint("auth"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("mullvad: auth: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return "", fmt.Errorf("mullvad: account number was rejected")
		}
		return "", fmt.Errorf("mullvad: auth HTTP %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("mullvad: auth: malformed response")
	}
	return out.AccessToken, nil
}

func (m *Mullvad) createDevice(ctx context.Context, token, pubkey string) (apiDevice, error) {
	var zero apiDevice
	body, _ := json.Marshal(map[string]any{"pubkey": pubkey, "hijack_dns": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint("devices"), bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == 400 || resp.StatusCode == 429 {
			return zero, fmt.Errorf("mullvad: could not register a device (HTTP %d). The account may be at its device limit — remove unused devices at https://mullvad.net/account. %s", resp.StatusCode, truncate(string(b), 80))
		}
		return zero, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(b), 120))
	}
	if err := json.Unmarshal(b, &zero); err != nil {
		return zero, err
	}
	if zero.IPv4 == "" {
		return zero, fmt.Errorf("device response missing ipv4_address")
	}
	return zero, nil
}

func (m *Mullvad) pick(ctx context.Context, n int) ([]Relay, error) {
	all, err := m.listRelays(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	type cand struct {
		r     Relay
		score int
	}
	var cs []cand
	for _, r := range all {
		if !r.Active || r.IPv4 == "" || r.PubKey == "" {
			continue
		}
		if m.used[r.IPv4] {
			continue
		}
		cs = append(cs, cand{r: r})
	}
	if len(cs) < n {
		return nil, fmt.Errorf("mullvad: only %d unused relays, need %d", len(cs), n)
	}
	// Prefer spreading across countries, then cities.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].r.Country != cs[j].r.Country {
			return cs[i].r.Country < cs[j].r.Country
		}
		return cs[i].r.Hostname < cs[j].r.Hostname
	})
	out := make([]Relay, 0, n)
	seenC := map[string]bool{}
	seenIP := map[string]bool{}
	take := func(filterCountry bool) {
		for _, c := range cs {
			if len(out) >= n {
				return
			}
			if seenIP[c.r.IPv4] {
				continue
			}
			if filterCountry && seenC[c.r.Country] {
				continue
			}
			out = append(out, c.r)
			seenC[c.r.Country] = true
			seenIP[c.r.IPv4] = true
			m.used[c.r.IPv4] = true
		}
	}
	take(true)
	take(false)
	if len(out) < n {
		return nil, fmt.Errorf("mullvad: could not pick %d distinct relays", n)
	}
	return out[:n], nil
}

func (m *Mullvad) listRelays(ctx context.Context) ([]Relay, error) {
	m.mu.Lock()
	if len(m.relays) > 0 {
		r := m.relays
		m.mu.Unlock()
		return r, nil
	}
	m.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint("relays"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mullvad: relays: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("mullvad: relays HTTP %d", resp.StatusCode)
	}
	var all []Relay
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&all); err != nil {
		return nil, fmt.Errorf("mullvad: relays: %w", err)
	}
	m.mu.Lock()
	m.relays = all
	m.mu.Unlock()
	return all, nil
}

// Login verifies the account number, writes provider.toml, and registers
// devices for slots 1..n. Existing devices are kept (--fresh does not call this).
func Login(ctx context.Context, path, account string, n int) error {
	account = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, account)
	if len(account) < 10 {
		return fmt.Errorf("mullvad account number looks too short")
	}
	f := File{Type: "mullvad", Account: account}
	if existing, err := LoadFile(path); err == nil && strings.EqualFold(existing.Type, "mullvad") && existing.Account == account {
		f.Device = existing.Device
	}
	if err := SaveFile(path, f); err != nil {
		return err
	}
	m, err := OpenMullvad(ctx, path, n)
	if err != nil {
		return err
	}
	_ = m
	return nil
}

func stripCIDR(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// Logout removes local provider.toml. Mullvad devices are left on the
// account so a later login can reuse them if the same file is restored;
// --fresh never calls this.
func Logout(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RedactAccount shows only the last four digits of a Mullvad account number.
func RedactAccount(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(s) < 4 {
		return "****"
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// LoginTimeout is the default timeout for provider login.
const LoginTimeout = 45 * time.Second
