package cryptox

import (
	"encoding/hex"
	"strings"
	"testing"
)

var testKey = mustKey("c0a30a13f6a96bedfb35a92b8d2dab2d4d1c5b6a9e0f2c3d8a6b7c1d2e3f4a5c")

func mustKey(h string) []byte {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	return b
}

func TestSHA256Hex(t *testing.T) {
	got := SHA256Hex("sk-lr-test")
	// reference value from `printf 'sk-lr-test' | sha256sum`
	const want = "b0a5999ebde40acc40ad597a7a8ba6def11f0f1b1f2f8a4a3d93f0b573e53724"
	if got != want {
		t.Fatalf("SHA256Hex: got %s, want %s", got, want)
	}
}

func TestGenerateAPIKey(t *testing.T) {
	plain, hashHex, prefix, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "sk-lr-") {
		t.Fatalf("missing sk-lr- prefix: %s", plain)
	}
	// Matches Node generator: 6 (sk-lr-) + 27 (base64url of 20B).
	if got, want := len(plain), 33; got != want {
		t.Fatalf("plaintext length: got %d, want %d", got, want)
	}
	if len(hashHex) != 64 {
		t.Fatalf("hashHex length: got %d, want 64", len(hashHex))
	}
	if !strings.HasPrefix(plain, prefix) || len(prefix) != 14 {
		t.Fatalf("prefix mismatch: %q vs %q", prefix, plain)
	}
	if SHA256Hex(plain) != hashHex {
		t.Fatal("hash does not match sha256(plaintext)")
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"flat", map[string]any{"api_key": "sk-abc", "weight": float64(2)}},
		{"oauth", map[string]any{"access_token": "x", "refresh_token": "y", "scopes": []any{"a", "b"}}},
		{"nested", map[string]any{"meta": map[string]any{"k": "v"}, "n": float64(1)}},
		{"empty", map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blob, err := EncryptForTenant(testKey, 42, c.input)
			if err != nil {
				t.Fatal(err)
			}
			if blob[0] != 0x01 {
				t.Fatalf("version byte: got %#x, want 0x01", blob[0])
			}
			if len(blob) < 1+12+16 {
				t.Fatalf("blob too short: %d", len(blob))
			}
			got, err := DecryptForTenant(testKey, 42, blob)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.input) {
				t.Fatalf("key count mismatch: got %d, want %d", len(got), len(c.input))
			}
			for k := range c.input {
				if _, ok := got[k]; !ok {
					t.Fatalf("missing key %q after decrypt", k)
				}
			}
		})
	}
}

func TestEnvelopeWrongTenant(t *testing.T) {
	blob, err := EncryptForTenant(testKey, 1, map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptForTenant(testKey, 2, blob); err == nil {
		t.Fatal("decrypt with wrong tenant ID should fail")
	}
}

func TestEnvelopeWrongVersion(t *testing.T) {
	bad := []byte{0x99, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := DecryptForTenant(testKey, 1, bad); err == nil {
		t.Fatal("decrypt with wrong version byte should fail")
	}
}
