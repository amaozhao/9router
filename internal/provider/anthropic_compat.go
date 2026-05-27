package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/amaozhao/lazirouter/internal/errs"
)

// AnthropicCompat speaks Anthropic /v1/messages against a third-party endpoint
// that mimics Anthropic's protocol (e.g. MiniMax Token Plan at
// https://api.minimaxi.com/anthropic). Auth is a plain API key sent via the
// canonical x-api-key header; no body cloaking is applied — the request is
// forwarded verbatim.
type AnthropicCompat struct {
	mu          sync.Mutex
	dispatchers map[string]*http.Client
}

func NewAnthropicCompat() *AnthropicCompat {
	return &AnthropicCompat{dispatchers: map[string]*http.Client{}}
}

// ChatMessages POSTs body to baseURL+/v1/messages without streaming.
func (a *AnthropicCompat) ChatMessages(ctx context.Context, baseURL, apiKey string, metadata map[string]any, body map[string]any) (map[string]any, error) {
	out := cloneMapAny(body)
	out["stream"] = false
	raw, _ := json.Marshal(out)
	res, err := a.do(ctx, baseURL, apiKey, metadata, raw, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, errs.Upstream(fmt.Sprintf("AnthropicCompat %d: %s", res.StatusCode, snippet(buf)),
			map[string]any{"status_code": res.StatusCode, "body": string(buf)})
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf, &parsed); err != nil {
		return nil, errs.Upstream("non-JSON response", map[string]any{"body": snippet(buf)})
	}
	return parsed, nil
}

// StreamMessages POSTs with stream=true and yields raw SSE frames.
func (a *AnthropicCompat) StreamMessages(ctx context.Context, baseURL, apiKey string, metadata map[string]any, body map[string]any) (<-chan []byte, <-chan error) {
	data, errc := make(chan []byte, 32), make(chan error, 1)
	go func() {
		defer close(data)
		out := cloneMapAny(body)
		out["stream"] = true
		raw, _ := json.Marshal(out)
		res, err := a.do(ctx, baseURL, apiKey, metadata, raw, "text/event-stream")
		if err != nil {
			errc <- err
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			buf, _ := io.ReadAll(res.Body)
			errc <- errs.Upstream(fmt.Sprintf("AnthropicCompat %d: %s", res.StatusCode, snippet(buf)),
				map[string]any{"status_code": res.StatusCode, "body": string(buf)})
			return
		}
		writeFrames(ctx, res.Body, data)
	}()
	return data, errc
}

func (a *AnthropicCompat) do(ctx context.Context, baseURL, apiKey string, metadata map[string]any, body []byte, accept string) (*http.Response, error) {
	url := strings.TrimRight(baseURL, "/") + "/v1/messages"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	cli := proxyClient(&a.mu, a.dispatchers, metadata)
	res, err := cli.Do(req)
	if err != nil {
		return nil, errs.Upstream("anthropic-compat upstream call failed: " + err.Error())
	}
	return res, nil
}
