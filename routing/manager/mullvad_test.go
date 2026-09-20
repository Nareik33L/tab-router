package manager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Nareik33L/tab-router/routing/wireguard"
)

func TestMullvadProvisionPicksDistinctRelays(t *testing.T) {
	var created int
	pubs := make([]string, 3)
	for i := range pubs {
		k, err := wireguard.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		pubs[i] = k.Public().Base64()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/v1/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"tok"}`))
	})
	mux.HandleFunc("/accounts/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		created++
		fmt.Fprintf(w, `{"id":"d%d","name":"dev-%d","ipv4_address":"10.64.0.%d/32","ipv6_address":"fc00::%d/128"}`, created, created, created, created)
	})
	mux.HandleFunc("/www/relays/wireguard", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[
			{"hostname":"se1","country_code":"se","city_code":"sto","active":true,"ipv4_addr_in":"203.0.113.10","pubkey":%q},
			{"hostname":"de1","country_code":"de","city_code":"ber","active":true,"ipv4_addr_in":"198.51.100.7","pubkey":%q},
			{"hostname":"nl1","country_code":"nl","city_code":"ams","active":true,"ipv4_addr_in":"192.0.2.9","pubkey":%q}
		]`, pubs[0], pubs[1], pubs[2])
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	path := filepath.Join(t.TempDir(), "provider.toml")
	if err := SaveFile(path, File{Type: "mullvad", Account: "1234567890123456"}); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := &Mullvad{
		path: path, file: f, http: ts.Client(), used: map[string]bool{},
		authURL: ts.URL + "/auth/v1/token", devicesURL: ts.URL + "/accounts/v1/devices",
		relaysURL: ts.URL + "/www/relays/wireguard",
	}
	ctx := context.Background()
	if err := m.ensureDevices(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Fatalf("created %d devices, want 2", created)
	}
	// Second ensure must not hit the API.
	if err := m.ensureDevices(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Fatalf("ensureDevices recreated devices: %d", created)
	}
	loaded, err := LoadFile(path)
	if err != nil || len(loaded.Device) != 2 {
		t.Fatalf("devices not persisted: %+v %v", loaded.Device, err)
	}
	if loaded.Device[0].IPv4 != "10.64.0.1" {
		t.Fatalf("CIDR not stripped: %q", loaded.Device[0].IPv4)
	}

	routes, err := m.Provision(ctx, 2)
	if err != nil || len(routes) != 2 {
		t.Fatalf("Provision: %v %d", err, len(routes))
	}
	a, b := routes[0].Def(), routes[1].Def()
	if a.Address == b.Address || a.PeerPublicKey == b.PeerPublicKey {
		t.Fatalf("identities share a relay: %+v %+v", a, b)
	}
	if a.Type != "wireguard" || a.PrivateKey == "" {
		t.Fatalf("unexpected def: %+v", a)
	}
	if a.LocalAddress == loaded.Device[1].IPv4 {
		t.Fatal("identity 001 bound to identity 002's tunnel IP")
	}

	repl, err := m.Reestablish(ctx, 1, routes[0])
	if err != nil {
		t.Fatal(err)
	}
	if repl.Def().Address == a.Address {
		t.Fatalf("re-establish reused the failed relay %s", a.Address)
	}
	if repl.Def().Address == b.Address {
		t.Fatal("re-establish stole identity 002's relay")
	}
}
