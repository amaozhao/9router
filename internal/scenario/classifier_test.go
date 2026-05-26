package scenario

import "testing"

func TestClassifyPriority(t *testing.T) {
	long := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		long = append(long, map[string]any{
			"role":    "user",
			"content": stringOfLen(40000),
		})
	}

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"empty → default", map[string]any{}, Default},
		{"tools only → tool_use", map[string]any{
			"tools": []any{map[string]any{"function": map[string]any{"name": "calc"}}},
		}, ToolUse},
		{"web tool wins over tool_use", map[string]any{
			"tools": []any{map[string]any{"function": map[string]any{"name": "web_search"}}},
		}, Web},
		{"anthropic-shape web tool", map[string]any{
			"tools": []any{map[string]any{"name": "browse"}},
		}, Web},
		{"image content → vision", map[string]any{
			"messages": []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url"},
					map[string]any{"type": "text", "text": "what is this"},
				},
			}},
		}, Vision},
		{"long context > 256k chars", map[string]any{"messages": long}, LongContext},
		{"reasoning_effort high → think", map[string]any{
			"reasoning_effort": "high",
			"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
		}, Think},
		{"thinking.enabled → think", map[string]any{
			"thinking": map[string]any{"enabled": true},
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}, Think},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.body); got != c.want {
				t.Fatalf("Classify: got %q, want %q", got, c.want)
			}
		})
	}
}

func stringOfLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
