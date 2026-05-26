package jwtx

import (
	"strings"
	"testing"
)

var secret = []byte("test-secret")

func TestSignVerifyRoundTrip(t *testing.T) {
	tok, err := Sign(secret, Session{UserID: 7, TenantID: 42, Role: "owner", Email: "u@v"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(tok, ".") != 2 {
		t.Fatalf("not a JWS compact: %q", tok)
	}
	out, err := Verify(secret, tok)
	if err != nil {
		t.Fatal(err)
	}
	if out.UserID != 7 || out.TenantID != 42 || out.Role != "owner" || out.Email != "u@v" {
		t.Fatalf("payload mismatch: %+v", out)
	}
	if out.ExpiresAt == nil || out.ExpiresAt.Time.IsZero() {
		t.Fatal("missing exp claim")
	}
	if out.IssuedAt == nil {
		t.Fatal("missing iat claim")
	}
}

func TestVerifyWrongSecret(t *testing.T) {
	tok, _ := Sign(secret, Session{UserID: 1, TenantID: 1, Role: "member"})
	if _, err := Verify([]byte("other"), tok); err == nil {
		t.Fatal("expected verification failure with wrong secret")
	}
}

func TestVerifyTamperedToken(t *testing.T) {
	tok, _ := Sign(secret, Session{UserID: 1, TenantID: 1, Role: "member"})
	parts := strings.Split(tok, ".")
	parts[1] += "tamper"
	if _, err := Verify(secret, strings.Join(parts, ".")); err == nil {
		t.Fatal("expected verification failure on tampered payload")
	}
}

func TestVerifyMalformed(t *testing.T) {
	if _, err := Verify(secret, "not-a-jwt"); err == nil {
		t.Fatal("expected error for malformed token")
	}
}
