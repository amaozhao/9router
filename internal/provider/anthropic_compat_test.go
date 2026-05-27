package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicCompat_ChatMessages(t *testing.T) {
	var gotPath, gotKey, gotVer, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		gotCT = r.Header.Get("Content-Type")
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":3}}`))
	}))
	defer srv.Close()

	a := NewAnthropicCompat()
	out, err := a.ChatMessages(context.Background(), srv.URL, "sk-test-123", nil, map[string]any{
		"model":      "MiniMax-M1",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens": 100,
	})
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path: %s", gotPath)
	}
	if gotKey != "sk-test-123" {
		t.Fatalf("x-api-key: %s", gotKey)
	}
	if gotVer != "2023-06-01" {
		t.Fatalf("anthropic-version: %s", gotVer)
	}
	if gotCT != "application/json" {
		t.Fatalf("content-type: %s", gotCT)
	}
	if gotBody["stream"] != false {
		t.Fatalf("stream should be forced false on non-stream path: %v", gotBody["stream"])
	}
	if gotBody["model"] != "MiniMax-M1" {
		t.Fatalf("model should be forwarded: %v", gotBody["model"])
	}
	if id, _ := out["id"].(string); id != "msg_1" {
		t.Fatalf("response passthrough: %+v", out)
	}
}

func TestAnthropicCompat_StreamMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &body)
		if body["stream"] != true {
			t.Errorf("stream flag should be true, got %v", body["stream"])
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	a := NewAnthropicCompat()
	frames, errc := a.StreamMessages(context.Background(), srv.URL, "k", nil, map[string]any{"model": "x"})
	var combined strings.Builder
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				goto done
			}
			combined.Write(f)
		case err := <-errc:
			t.Fatalf("stream error: %v", err)
		}
	}
done:
	s := combined.String()
	for _, want := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in stream output:\n%s", want, s)
		}
	}
}

func TestAnthropicCompat_4xx_PropagatesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()
	a := NewAnthropicCompat()
	_, err := a.ChatMessages(context.Background(), srv.URL, "k", nil, map[string]any{"model": "x"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention status: %v", err)
	}
}
