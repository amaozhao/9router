package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicToOpenAIRequest_String(t *testing.T) {
	body := map[string]any{
		"model":      "claude-3-5-sonnet",
		"system":     "you are friendly",
		"max_tokens": float64(123),
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	got := AnthropicToOpenAIRequest(body)
	msgs := got["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "you are friendly" {
		t.Fatalf("system slot: %+v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "hello" {
		t.Fatalf("user slot: %+v", msgs[1])
	}
	if got["max_tokens"] != float64(123) {
		t.Fatalf("max_tokens missing")
	}
}

func TestAnthropicToOpenAIRequest_AllText(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "abc"},
				map[string]any{"type": "text", "text": "def"},
			}},
		},
	}
	got := AnthropicToOpenAIRequest(body)
	msgs := got["messages"].([]map[string]any)
	if msgs[0]["content"] != "abcdef" {
		t.Fatalf("all-text fast-path: got %v", msgs[0]["content"])
	}
}

func TestAnthropicToOpenAIRequest_Tools(t *testing.T) {
	body := map[string]any{
		"messages": []any{},
		"tools": []any{
			map[string]any{"name": "calc", "description": "math", "input_schema": map[string]any{"x": "y"}},
		},
	}
	got := AnthropicToOpenAIRequest(body)
	tools := got["tools"].([]map[string]any)
	if tools[0]["type"] != "function" {
		t.Fatalf("type: %v", tools[0]["type"])
	}
	fn := tools[0]["function"].(map[string]any)
	if fn["name"] != "calc" || fn["description"] != "math" {
		t.Fatalf("function block: %+v", fn)
	}
}

func TestOpenAIToAnthropicResponse(t *testing.T) {
	o := map[string]any{
		"id": "chatcmpl-xyz",
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": "ok"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": float64(7), "completion_tokens": float64(3)},
	}
	got := OpenAIToAnthropicResponse(o, "claude-3-5-sonnet")
	if got["type"] != "message" || got["role"] != "assistant" {
		t.Fatalf("shape: %+v", got)
	}
	if got["model"] != "claude-3-5-sonnet" {
		t.Fatalf("requestedModel pass-through: %v", got["model"])
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != int64(7) || usage["output_tokens"] != int64(3) {
		t.Fatalf("usage: %+v", usage)
	}
	if got["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason mapping: %v", got["stop_reason"])
	}
}

func TestStreamTranslator(t *testing.T) {
	st := NewStream("claude-test")
	frame := []byte(`data: {"id":"x","choices":[{"delta":{"content":"hi"}}]}

`)
	out := st.Feed(frame)
	if len(out) < 3 {
		t.Fatalf("expected message_start + content_block_start + delta, got %d frames", len(out))
	}
	// First event should be message_start
	if !strings.Contains(string(out[0]), "message_start") {
		t.Fatalf("first frame: %s", out[0])
	}
	// Feed the stop frame
	stop := []byte(`data: {"id":"x","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}

`)
	out2 := st.Feed(stop)
	// Should emit content_block_stop + message_delta + message_stop
	combined := string(joinAll(out2))
	for _, want := range []string{"content_block_stop", "message_delta", "message_stop", `"input_tokens":4`, `"output_tokens":2`} {
		if !strings.Contains(combined, want) {
			t.Fatalf("missing %q in stop frames:\n%s", want, combined)
		}
	}
	// Flush after in-band stop should be a no-op
	if extra := st.Flush(); len(extra) != 0 {
		t.Fatalf("Flush should be idempotent after finish_reason; got %d frames", len(extra))
	}
}

func TestStreamTranslator_FlushWhenNoFinish(t *testing.T) {
	st := NewStream("m")
	st.Feed([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}

`))
	flush := st.Flush()
	combined := string(joinAll(flush))
	for _, want := range []string{"content_block_stop", "message_delta", "message_stop"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("missing %q in flush:\n%s", want, combined)
		}
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"content_filter", "stop_sequence"},
		{"unknown", "end_turn"},
		{"", nil},
	}
	for _, c := range cases {
		got := MapFinishReason(c.in)
		if !equalAny(got, c.want) {
			t.Fatalf("MapFinishReason(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSanitizeReadToolArgs(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "drop empty pages on non-pdf",
			in:   map[string]any{"file_path": "/tmp/a.txt", "pages": ""},
			want: map[string]any{"file_path": "/tmp/a.txt"},
		},
		{
			name: "drop pages on non-pdf even when shaped right",
			in:   map[string]any{"file_path": "/tmp/a.txt", "pages": "1-5"},
			want: map[string]any{"file_path": "/tmp/a.txt"},
		},
		{
			name: "keep valid pages on pdf",
			in:   map[string]any{"file_path": "/tmp/a.pdf", "pages": "1-5"},
			want: map[string]any{"file_path": "/tmp/a.pdf", "pages": "1-5"},
		},
		{
			name: "drop malformed pages on pdf",
			in:   map[string]any{"file_path": "/tmp/a.pdf", "pages": "abc"},
			want: map[string]any{"file_path": "/tmp/a.pdf"},
		},
		{
			name: "coerce numeric-string limit + clamp >2000",
			in:   map[string]any{"limit": "5000"},
			want: map[string]any{"limit": float64(2000)},
		},
		{
			name: "drop limit < 1",
			in:   map[string]any{"limit": float64(0)},
			want: map[string]any{},
		},
		{
			name: "coerce negative offset to 0",
			in:   map[string]any{"offset": float64(-3)},
			want: map[string]any{"offset": float64(0)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sanitizeToolInput("Read", c.in)
			if !equalAny(c.in, c.want) {
				t.Fatalf("got %+v, want %+v", c.in, c.want)
			}
		})
	}
}

func TestOpenAIToAnthropicResponse_SanitizesReadArgs(t *testing.T) {
	o := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_1",
					"function": map[string]any{
						"name":      "Read",
						"arguments": `{"file_path":"/tmp/x.txt","pages":"","limit":"100"}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
	}
	got := OpenAIToAnthropicResponse(o, "claude-3-5-sonnet")
	content := got["content"].([]map[string]any)
	if len(content) != 1 {
		t.Fatalf("content length: %d", len(content))
	}
	input := content[0]["input"].(map[string]any)
	if _, has := input["pages"]; has {
		t.Fatalf("empty pages should be dropped: %+v", input)
	}
	if input["limit"] != float64(100) {
		t.Fatalf("limit should coerce numeric string: %+v", input["limit"])
	}
}

// Helpers
func joinAll(b [][]byte) []byte {
	out := []byte{}
	for _, x := range b {
		out = append(out, x...)
	}
	return out
}
func equalAny(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
