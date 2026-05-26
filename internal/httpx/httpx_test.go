package httpx

import (
	"net/http"
	"testing"
)

func TestDefaultBaseURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"openai", "https://api.openai.com"},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"glm", "https://open.bigmodel.cn/api/paas/v4"},
		{"deepseek", "https://api.deepseek.com"},
		{"minimax", "https://api.minimaxi.com"},
		{"anthropic", "https://api.anthropic.com"},
		{"unknown", ""},
	} {
		if got := DefaultBaseURL(c.in); got != c.want {
			t.Fatalf("DefaultBaseURL(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBearerOrXAPIKey(t *testing.T) {
	r1, _ := http.NewRequest("GET", "/", nil)
	r1.Header.Set("Authorization", "Bearer sk-lr-aaa")
	if got := BearerOrXAPIKey(r1); got != "sk-lr-aaa" {
		t.Fatalf("Bearer: got %q", got)
	}

	r2, _ := http.NewRequest("GET", "/", nil)
	r2.Header.Set("x-api-key", "sk-lr-bbb")
	if got := BearerOrXAPIKey(r2); got != "sk-lr-bbb" {
		t.Fatalf("x-api-key: got %q", got)
	}

	r3, _ := http.NewRequest("GET", "/", nil)
	if got := BearerOrXAPIKey(r3); got != "" {
		t.Fatalf("empty: got %q", got)
	}

	// Bearer wins over x-api-key when both present
	r4, _ := http.NewRequest("GET", "/", nil)
	r4.Header.Set("Authorization", "Bearer A")
	r4.Header.Set("x-api-key", "B")
	if got := BearerOrXAPIKey(r4); got != "A" {
		t.Fatalf("precedence: got %q, want A", got)
	}
}

func TestStringFrom(t *testing.T) {
	m := map[string]any{"s": "hi", "n": 42, "nilv": nil}
	if got := StringFrom(m, "s"); got != "hi" {
		t.Fatalf("hit: %q", got)
	}
	if got := StringFrom(m, "n"); got != "" {
		t.Fatalf("wrong type: %q", got)
	}
	if got := StringFrom(m, "nilv"); got != "" {
		t.Fatalf("nil: %q", got)
	}
	if got := StringFrom(m, "missing"); got != "" {
		t.Fatalf("missing: %q", got)
	}
}
