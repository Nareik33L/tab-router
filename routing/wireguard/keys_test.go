package wireguard

import (
	"encoding/base64"
	"testing"
)

func TestKeyRoundTrip(t *testing.T) {
	priv, err := GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public()
	for _, enc := range []string{priv.Hex(), priv.Base64()} {
		got, err := ParseKey(enc)
		if err != nil || got != priv {
			t.Fatalf("ParseKey(%q) = %v %v", enc, got, err)
		}
	}
	if _, err := ParseKey(""); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := ParseKey("not-a-key"); err == nil {
		t.Fatal("garbage key accepted")
	}
	if _, err := ParseKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short key accepted")
	}
	if pub == priv {
		t.Fatal("public key equalled private key")
	}
}
