package provider

import (
	"reflect"
	"testing"
)

func TestSanitizeCodexInput_StripsServerIDs(t *testing.T) {
	in := []any{
		"rs_abc123",      // bare server id → drop
		"resp_xyz",       // bare server id → drop
		"hello",          // keep
		map[string]any{"type": "item_reference", "id": "fc_99"}, // drop item_reference
		map[string]any{"id": "msg_42", "role": "user", "content": "hi"}, // strip id, keep rest
		map[string]any{"id": "user_42", "role": "user", "content": "hi"}, // non-server id preserved
	}
	got := sanitizeCodexInput(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 surviving items, got %d: %+v", len(got), got)
	}
	if got[0] != "hello" {
		t.Fatalf("first survivor: %v", got[0])
	}
	if m, _ := got[1].(map[string]any); m == nil || m["id"] != nil || m["content"] != "hi" {
		t.Fatalf("server id should be stripped, content preserved: %+v", got[1])
	}
	if m, _ := got[2].(map[string]any); m == nil || m["id"] != "user_42" {
		t.Fatalf("non-server id should be preserved: %+v", got[2])
	}
}

func TestSanitizeCodexInput_SystemToDeveloper(t *testing.T) {
	in := []any{
		map[string]any{"role": "system", "content": "you are X"},
		map[string]any{"role": "system", "type": "message", "content": "explicit type"},
		map[string]any{"role": "system", "type": "function_call_output", "output": "tool"},
		map[string]any{"role": "user", "content": "hi"},
	}
	got := sanitizeCodexInput(in)
	if got[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("plain system should become developer: %+v", got[0])
	}
	if got[1].(map[string]any)["role"] != "developer" {
		t.Fatalf("system with type=message should become developer: %+v", got[1])
	}
	if got[2].(map[string]any)["role"] != "system" {
		t.Fatalf("system with non-message type must NOT be rewritten: %+v", got[2])
	}
	if got[3].(map[string]any)["role"] != "user" {
		t.Fatalf("user role left alone: %+v", got[3])
	}
}

func TestCodexCloakBody_AppliesInputSanitizer(t *testing.T) {
	body := map[string]any{
		"model":       "gpt-5",
		"temperature": 0.7,
		"input": []any{
			"rs_dropme",
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	out := codexCloakBody(body)
	if out["store"] != false || out["stream"] != true {
		t.Fatalf("store/stream invariants: %+v", out)
	}
	if _, has := out["temperature"]; has {
		t.Fatalf("temperature should be stripped")
	}
	input := out["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("rs_ prefix should be dropped: %+v", input)
	}
	if input[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("system→developer: %+v", input[0])
	}
	// reasoning envelope auto-injected
	rs, ok := out["reasoning"].(map[string]any)
	if !ok || rs["summary"] != "auto" {
		t.Fatalf("reasoning envelope: %+v", out["reasoning"])
	}
	// non-trivial effort auto-adds include
	if !reflect.DeepEqual(out["include"], []any{"reasoning.encrypted_content"}) {
		t.Fatalf("include auto-add: %+v", out["include"])
	}
}
