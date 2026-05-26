// Package translator converts Anthropic /v1/messages bodies to OpenAI
// /v1/chat/completions bodies and back. Mirrors router/src/translator/anthropic.js.
package translator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// AnthropicToOpenAIRequest takes an Anthropic body and returns an OpenAI body.
// Both are loose map[string]any to avoid the type ceremony.
func AnthropicToOpenAIRequest(a map[string]any) map[string]any {
	messages := []map[string]any{}

	if sys, ok := a["system"]; ok && sys != nil {
		var text string
		switch v := sys.(type) {
		case string:
			text = v
		case []any:
			parts := []string{}
			for _, x := range v {
				if b, ok := x.(map[string]any); ok {
					if b["type"] == "text" {
						parts = append(parts, asString(b["text"]))
					}
				}
			}
			text = strings.Join(parts, "\n")
		}
		if text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}

	if msgs, ok := a["messages"].([]any); ok {
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			messages = append(messages, map[string]any{
				"role":    mm["role"],
				"content": contentBlocksToOpenAI(mm["content"]),
			})
		}
	}

	out := map[string]any{
		"model":    a["model"],
		"messages": messages,
		"stream":   truthy(a["stream"]),
	}
	for _, k := range []string{"max_tokens", "temperature", "top_p"} {
		if v, ok := a[k]; ok && v != nil {
			out[k] = v
		}
	}
	if v, ok := a["stop_sequences"]; ok && v != nil {
		out["stop"] = v
	}
	if tools, ok := a["tools"].([]any); ok && len(tools) > 0 {
		converted := []map[string]any{}
		for _, t := range tools {
			tt, _ := t.(map[string]any)
			converted = append(converted, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tt["name"],
					"description": tt["description"],
					"parameters":  tt["input_schema"],
				},
			})
		}
		out["tools"] = converted
	}
	return out
}

func contentBlocksToOpenAI(c any) any {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		// All-text fast path → flatten to a single string.
		allText := true
		for _, b := range v {
			bb, ok := b.(map[string]any)
			if !ok || bb["type"] != "text" {
				allText = false
				break
			}
		}
		if allText {
			parts := []string{}
			for _, b := range v {
				parts = append(parts, asString(b.(map[string]any)["text"]))
			}
			return strings.Join(parts, "")
		}
		out := []map[string]any{}
		for _, b := range v {
			bb, _ := b.(map[string]any)
			switch bb["type"] {
			case "text":
				out = append(out, map[string]any{"type": "text", "text": bb["text"]})
			case "image":
				src, _ := bb["source"].(map[string]any)
				var url string
				if src != nil && src["data"] != nil {
					url = "data:" + asString(src["media_type"]) + ";base64," + asString(src["data"])
				} else if src != nil {
					url = asString(src["url"])
				}
				out = append(out, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": url},
				})
			case "tool_use":
				txt, _ := json.Marshal(bb["input"])
				out = append(out, map[string]any{
					"type": "text",
					"text": "<tool_use:" + asString(bb["name"]) + " " + string(txt) + ">",
				})
			case "tool_result":
				if s, ok := bb["content"].(string); ok {
					out = append(out, map[string]any{"type": "text", "text": s})
				} else {
					b, _ := json.Marshal(bb["content"])
					out = append(out, map[string]any{"type": "text", "text": string(b)})
				}
			default:
				out = append(out, map[string]any{"type": "text", "text": ""})
			}
		}
		return out
	}
	if c == nil {
		return ""
	}
	return asString(c)
}

// OpenAIToAnthropicResponse converts a non-streaming OpenAI chat completion
// response into an Anthropic message response.
func OpenAIToAnthropicResponse(o map[string]any, requestedModel string) map[string]any {
	choice := firstChoice(o)
	msg, _ := choice["message"].(map[string]any)
	content := []map[string]any{}
	if s, ok := msg["content"].(string); ok && s != "" {
		content = append(content, map[string]any{"type": "text", "text": s})
	}
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, t := range tcs {
			tc, _ := t.(map[string]any)
			fn, _ := tc["function"].(map[string]any)
			var input any = map[string]any{}
			if args, ok := fn["arguments"].(string); ok && args != "" {
				_ = json.Unmarshal([]byte(args), &input)
			}
			id, _ := tc["id"].(string)
			if id == "" {
				id = "toolu_" + randID(8)
			}
			content = append(content, map[string]any{
				"type": "tool_use", "id": id, "name": fn["name"], "input": input,
			})
		}
	}

	model := requestedModel
	if model == "" {
		model = asString(o["model"])
	}
	id := asString(o["id"])
	if id == "" {
		id = "msg_" + randID(10)
	} else {
		id = "msg_" + id
	}
	usage, _ := o["usage"].(map[string]any)
	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   MapFinishReason(asString(choice["finish_reason"])),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  toInt(getOrZero(usage, "prompt_tokens")),
			"output_tokens": toInt(getOrZero(usage, "completion_tokens")),
		},
	}
}

// MapFinishReason translates OpenAI finish_reason → Anthropic stop_reason.
func MapFinishReason(r string) any {
	switch r {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "stop_sequence"
	case "":
		return nil
	default:
		return "end_turn"
	}
}

// StreamTranslator is the stateful per-request streaming translator.
type StreamTranslator struct {
	requestedModel string
	messageID      string
	started        bool
	blockOpen      bool
	stopped        bool // message_stop already emitted (in-band finish_reason)
	lastUsage      map[string]any
}

// NewStream constructs a translator for one request.
func NewStream(requestedModel string) *StreamTranslator {
	return &StreamTranslator{
		requestedModel: requestedModel,
		messageID:      "msg_" + randID(10),
		lastUsage:      map[string]any{"input_tokens": 0, "output_tokens": 0},
	}
}

// Feed processes one or more "data: ..." SSE lines and returns Anthropic frames.
func (st *StreamTranslator) Feed(rawFrame []byte) [][]byte {
	out := [][]byte{}
	for _, line := range strings.Split(string(rawFrame), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			continue
		}
		if !st.started {
			st.started = true
			model := st.requestedModel
			if model == "" {
				model = asString(obj["model"])
			}
			out = append(out, sse("message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": st.messageID, "type": "message", "role": "assistant",
					"content": []any{}, "model": model,
					"stop_reason": nil, "stop_sequence": nil,
					"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
				},
			}))
		}
		choice := firstChoice(obj)
		delta, _ := choice["delta"].(map[string]any)
		if dc, ok := delta["content"].(string); ok && dc != "" {
			if !st.blockOpen {
				out = append(out, sse("content_block_start", map[string]any{
					"type":          "content_block_start",
					"index":         0,
					"content_block": map[string]any{"type": "text", "text": ""},
				}))
				st.blockOpen = true
			}
			out = append(out, sse("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": dc},
			}))
		}
		if u, ok := obj["usage"].(map[string]any); ok {
			if v, ok := u["prompt_tokens"]; ok {
				st.lastUsage["input_tokens"] = toInt(v)
			}
			if v, ok := u["completion_tokens"]; ok {
				st.lastUsage["output_tokens"] = toInt(v)
			}
		}
		if fr := asString(choice["finish_reason"]); fr != "" {
			if st.blockOpen {
				out = append(out, sse("content_block_stop", map[string]any{
					"type": "content_block_stop", "index": 0,
				}))
				st.blockOpen = false
			}
			out = append(out, sse("message_delta", map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": MapFinishReason(fr), "stop_sequence": nil},
				"usage": st.lastUsage,
			}))
			out = append(out, sse("message_stop", map[string]any{"type": "message_stop"}))
			st.stopped = true
		}
	}
	return out
}

// Flush emits a clean close iff upstream finished without a finish_reason frame.
// No-op when Feed() already saw finish_reason (and therefore already emitted the
// closing message_delta + message_stop).
func (st *StreamTranslator) Flush() [][]byte {
	if st.stopped {
		return nil
	}
	out := [][]byte{}
	if st.blockOpen {
		out = append(out, sse("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": 0,
		}))
		st.blockOpen = false
	}
	if st.started {
		out = append(out, sse("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": st.lastUsage,
		}))
		out = append(out, sse("message_stop", map[string]any{"type": "message_stop"}))
		st.stopped = true
	}
	return out
}

func (st *StreamTranslator) Usage() (int64, int64) {
	return toInt(st.lastUsage["input_tokens"]), toInt(st.lastUsage["output_tokens"])
}

// ---------- helpers ----------

func sse(event string, payload map[string]any) []byte {
	body, _ := json.Marshal(payload)
	return []byte("event: " + event + "\ndata: " + string(body) + "\n\n")
}

func firstChoice(o map[string]any) map[string]any {
	c, ok := o["choices"].([]any)
	if !ok || len(c) == 0 {
		return map[string]any{}
	}
	m, _ := c[0].(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	case float64:
		// avoid scientific notation for integers
		if x == float64(int64(x)) {
			return jsonNumberStr(int64(x))
		}
	}
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonNumberStr(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false"
	}
	return false
}

func getOrZero(m map[string]any, k string) any {
	if m == nil {
		return 0
	}
	return m[k]
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}
