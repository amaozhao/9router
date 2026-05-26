package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/amaozhao/lazirouter/internal/errs"
)

// CodexSub speaks the OpenAI Responses API against ChatGPT's codex backend,
// using a chatgpt.com OAuth access_token + cloak headers + deterministic
// session_id. Same proxy plumbing as ClaudeSub.
type CodexSub struct {
	mu          sync.Mutex
	dispatchers map[string]*http.Client
}

func NewCodexSub() *CodexSub {
	return &CodexSub{dispatchers: map[string]*http.Client{}}
}

const codexURL = "https://chatgpt.com/backend-api/codex/responses"

var codexSpoofHeaders = map[string]string{
	"originator":  "codex_cli_rs",
	"user-agent":  "codex-cli/0.133.0 (linux; x64)",
	"openai-beta": "responses=experimental",
}

// SessionContext keys conversation/rate-limit state on (tenant, connection).
type SessionContext struct {
	TenantID     int64
	ConnectionID int64
}

func deriveSessionID(s SessionContext) string {
	sum := sha256.Sum256([]byte("9r:" + strconv.FormatInt(s.TenantID, 10) + ":" + strconv.FormatInt(s.ConnectionID, 10)))
	return "sess_" + hex.EncodeToString(sum[:])[:12]
}

// ResponsesCall is the non-stream path. Returns the raw upstream SSE text so
// callers can choose how to surface it (most just forward verbatim).
func (c *CodexSub) ResponsesCall(ctx context.Context, credentials, metadata map[string]any, body map[string]any, sc SessionContext) ([]byte, error) {
	tok := stringFrom(credentials, "access_token")
	if tok == "" {
		return nil, errs.Upstream("missing OAuth access_token")
	}
	cloaked := codexCloakBody(body)
	raw, _ := json.Marshal(cloaked)
	req := c.req(ctx, metadata, raw, tok, deriveSessionID(sc), "")
	cli := c.clientFor(metadata)
	res, err := cli.Do(req)
	if err != nil {
		return nil, errs.Upstream("codex upstream call failed: " + err.Error())
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, errs.Upstream(fmt.Sprintf("Codex %d: %s", res.StatusCode, snippet(buf)),
			map[string]any{"status_code": res.StatusCode, "body": string(buf)})
	}
	return buf, nil
}

// StreamResponses POSTs to the codex backend and yields each SSE frame.
func (c *CodexSub) StreamResponses(ctx context.Context, credentials, metadata map[string]any, body map[string]any, sc SessionContext) (<-chan []byte, <-chan error) {
	data, errc := make(chan []byte, 32), make(chan error, 1)
	go func() {
		defer close(data)
		tok := stringFrom(credentials, "access_token")
		if tok == "" {
			errc <- errs.Upstream("missing OAuth access_token")
			return
		}
		cloaked := codexCloakBody(body)
		raw, _ := json.Marshal(cloaked)
		req := c.req(ctx, metadata, raw, tok, deriveSessionID(sc), "text/event-stream")
		cli := c.clientFor(metadata)
		res, err := cli.Do(req)
		if err != nil {
			errc <- errs.Upstream("codex stream call failed: " + err.Error())
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			buf, _ := io.ReadAll(res.Body)
			errc <- errs.Upstream(fmt.Sprintf("Codex %d: %s", res.StatusCode, snippet(buf)),
				map[string]any{"status_code": res.StatusCode, "body": string(buf)})
			return
		}
		writeFrames(ctx, res.Body, data)
	}()
	return data, errc
}

func (c *CodexSub) req(ctx context.Context, metadata map[string]any, body []byte, tok, sessionID, accept string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, codexURL, bytes.NewReader(body))
	for k, v := range codexSpoofHeaders {
		req.Header.Set(k, v)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("session_id", sessionID)
	req.Header.Set("authorization", "Bearer "+tok)
	req.Header.Set("content-type", "application/json")
	return req
}

func (c *CodexSub) clientFor(metadata map[string]any) *http.Client {
	// Reuse the ClaudeSub clientFor logic via duplication-aware wrapper.
	sub := &ClaudeSub{dispatchers: c.dispatchers, mu: c.mu}
	cli := sub.clientFor(metadata)
	// Propagate any new dispatcher entry back into our map
	c.dispatchers = sub.dispatchers
	return cli
}

// codexCloakBody enforces the backend's invariants:
//   - store=false (sub tier rejects stored responses)
//   - stream=true (backend only emits SSE)
//   - strip OpenAI-Chat-Completions fields the backend 400s on
//   - inject reasoning envelope if missing
func codexCloakBody(body map[string]any) map[string]any {
	out := cloneMapAny(body)
	out["store"] = false
	out["stream"] = true
	for _, k := range []string{
		"temperature", "top_p", "frequency_penalty", "presence_penalty",
		"logprobs", "top_logprobs", "n", "seed",
		"max_tokens", "max_completion_tokens", "max_output_tokens",
		"user", "metadata", "stream_options", "prompt_cache_retention",
		"safety_identifier",
	} {
		delete(out, k)
	}
	rs, ok := out["reasoning"].(map[string]any)
	if !ok {
		rs = map[string]any{"effort": "low", "summary": "auto"}
		out["reasoning"] = rs
	} else if _, ok := rs["summary"]; !ok {
		rs["summary"] = "auto"
	}
	if effort, ok := rs["effort"].(string); ok && effort != "" && effort != "none" {
		if _, ok := out["include"]; !ok {
			out["include"] = []any{"reasoning.encrypted_content"}
		}
	}
	return out
}
