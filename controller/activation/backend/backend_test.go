package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testProxy() ProxyAccess {
	return ProxyAccess{Host: "gate.decodo.com", Port: 7000, Username: "limited", Password: "sentinel-password", Country: "gb", SessionMinutes: 1440}
}

func TestActivationLifecycle(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "licenses.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Issue(1, time.Now().Add(24*time.Hour), "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Store: store, Proxy: testProxy()}).Handler())
	t.Cleanup(srv.Close)

	res := post(t, srv.URL+"/v1/activate", map[string]string{"key": strings.ToLower(strings.ReplaceAll(key, "-", ""))})
	if res.Code != 200 {
		t.Fatalf("activate %d %s", res.Code, res.Body)
	}
	var act activateResponse
	decodeBody(t, res.Body, &act)
	if act.Proxy.Password != "sentinel-password" || act.Proxy.Username != "limited" || act.InstallationID == "" || act.Token == "" {
		t.Fatalf("bad activation %+v", act)
	}
	if strings.Contains(res.Body.String(), "master") {
		t.Fatal("response should not describe a master credential")
	}

	ok := post(t, srv.URL+"/v1/validate", map[string]string{"installation_id": act.InstallationID, "token": act.Token})
	if ok.Code != 200 {
		t.Fatalf("validate %d %s", ok.Code, ok.Body)
	}
	again := post(t, srv.URL+"/v1/activate", map[string]string{"key": key})
	if again.Code != http.StatusForbidden {
		t.Fatalf("second installation: %d %s", again.Code, again.Body)
	}
	same := post(t, srv.URL+"/v1/activate", map[string]string{"key": key, "installation_id": act.InstallationID})
	if same.Code != 200 {
		t.Fatalf("re-activate same installation: %d %s", same.Code, same.Body)
	}
	var renewed activateResponse
	decodeBody(t, same.Body, &renewed)
	act.Token = renewed.Token
	var validated activateResponse
	decodeBody(t, ok.Body, &validated)
	if validated.Proxy.Password != "sentinel-password" {
		t.Fatal("validate dropped proxy access")
	}

	gone := post(t, srv.URL+"/v1/deactivate", map[string]string{"installation_id": act.InstallationID, "token": act.Token})
	if gone.Code != 200 {
		t.Fatalf("deactivate %d %s", gone.Code, gone.Body)
	}
	revoked := post(t, srv.URL+"/v1/validate", map[string]string{"installation_id": act.InstallationID, "token": act.Token})
	if revoked.Code != http.StatusUnauthorized && revoked.Code != http.StatusForbidden {
		t.Fatalf("revoked installation still valid: %d %s", revoked.Code, revoked.Body)
	}
}

func TestActivationRejects(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Store: store, Proxy: testProxy()}).Handler())
	t.Cleanup(srv.Close)

	bad := post(t, srv.URL+"/v1/activate", map[string]string{"key": "NOPE-NOPE-NOPE-NOPE"})
	if bad.Code != http.StatusUnauthorized || !strings.Contains(bad.Body.String(), "invalid_key") {
		t.Fatalf("invalid key: %d %s", bad.Code, bad.Body)
	}
	if strings.Contains(bad.Body.String(), "NOPE") {
		t.Fatal("error echoed the key")
	}

	key, err := store.Issue(1, time.Now().Add(-time.Hour), "", "")
	if err != nil {
		t.Fatal(err)
	}
	exp := post(t, srv.URL+"/v1/activate", map[string]string{"key": key})
	if exp.Code != http.StatusForbidden || !strings.Contains(exp.Body.String(), "expired") {
		t.Fatalf("expired: %d %s", exp.Code, exp.Body)
	}

	live, err := store.Issue(1, time.Now().Add(time.Hour), "dedicated", "dedicated-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(live); err != nil {
		t.Fatal(err)
	}
	rev := post(t, srv.URL+"/v1/activate", map[string]string{"key": live})
	if rev.Code != http.StatusForbidden || !strings.Contains(rev.Body.String(), "revoked") {
		t.Fatalf("revoked: %d %s", rev.Code, rev.Body)
	}

	empty := &Server{Store: store, Proxy: ProxyAccess{}}
	key2, err := store.Issue(1, time.Now().Add(time.Hour), "", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/activate", strings.NewReader(`{"key":"`+key2+`"}`))
	empty.activate(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "dedicated-pass") {
		t.Fatalf("missing proxy: %d %s", rec.Code, rec.Body)
	}
}

func post(t *testing.T, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	rec := httptest.NewRecorder()
	rec.Code = res.StatusCode
	rec.Body.Write(raw)
	return rec
}

func decodeBody(t *testing.T, body *bytes.Buffer, dest any) {
	t.Helper()
	if err := json.Unmarshal(body.Bytes(), dest); err != nil {
		t.Fatal(err)
	}
}
