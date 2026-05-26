package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/google/uuid"
)

// ClaudeSub speaks the Anthropic /v1/messages API using a claude.ai
// OAuth access_token, plus the cloak headers + body cloaking that identify the
// caller as the official claude-cli. Without these, Anthropic returns 403.
type ClaudeSub struct {
	mu           sync.Mutex
	dispatchers  map[string]*http.Client
}

func NewClaudeSub() *ClaudeSub {
	return &ClaudeSub{dispatchers: map[string]*http.Client{}}
}

const (
	claudeBaseURL       = "https://api.anthropic.com"
	claudeVersion       = "2.1.92"
	claudeCCEntrypoint  = "sdk-cli"
)

var claudeSpoofHeaders = map[string]string{
	"anthropic-version": "2023-06-01",
	"anthropic-beta": "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14," +
		"context-management-2025-06-27,prompt-caching-scope-2026-01-05," +
		"advanced-tool-use-2025-11-20,effort-2025-11-24,structured-outputs-2025-12-15," +
		"fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28",
	"user-agent":      "claude-cli/2.1.92 (external, sdk-cli)",
	"accept":          "application/json",
	"accept-language": "*",
}

// ChatMessages POSTs the request to api.anthropic.com (non-stream).
func (c *ClaudeSub) ChatMessages(ctx context.Context, credentials, metadata map[string]any, body map[string]any) (map[string]any, error) {
	tok := stringFrom(credentials, "access_token")
	if tok == "" {
		return nil, errs.Upstream("missing OAuth access_token")
	}
	cloaked := claudeCloakBody(body, tok)
	cloaked["stream"] = false
	raw, _ := json.Marshal(cloaked)
	res, err := c.do(ctx, metadata, raw, tok, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, errs.Upstream(fmt.Sprintf("Claude %d: %s", res.StatusCode, snippet(buf)),
			map[string]any{"status_code": res.StatusCode, "body": string(buf)})
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, errs.Upstream("non-JSON response", map[string]any{"body": snippet(buf)})
	}
	return out, nil
}

// StreamMessages POSTs with stream=true and yields raw SSE frames.
func (c *ClaudeSub) StreamMessages(ctx context.Context, credentials, metadata map[string]any, body map[string]any) (<-chan []byte, <-chan error) {
	data, errc := make(chan []byte, 32), make(chan error, 1)
	go func() {
		defer close(data)
		tok := stringFrom(credentials, "access_token")
		if tok == "" {
			errc <- errs.Upstream("missing OAuth access_token")
			return
		}
		cloaked := claudeCloakBody(body, tok)
		cloaked["stream"] = true
		raw, _ := json.Marshal(cloaked)
		res, err := c.do(ctx, metadata, raw, tok, "text/event-stream")
		if err != nil {
			errc <- err
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			buf, _ := io.ReadAll(res.Body)
			errc <- errs.Upstream(fmt.Sprintf("Claude %d: %s", res.StatusCode, snippet(buf)),
				map[string]any{"status_code": res.StatusCode, "body": string(buf)})
			return
		}
		writeFrames(ctx, res.Body, data)
	}()
	return data, errc
}

func (c *ClaudeSub) do(ctx context.Context, metadata map[string]any, body []byte, tok, accept string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, claudeBaseURL+"/v1/messages", bytes.NewReader(body))
	for k, v := range claudeSpoofHeaders {
		req.Header.Set(k, v)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	cli := c.clientFor(metadata)
	res, err := cli.Do(req)
	if err != nil {
		return nil, errs.Upstream("claude upstream call failed: " + err.Error())
	}
	return res, nil
}

// clientFor returns an *http.Client respecting per-connection proxy_url or
// HTTPS_PROXY env (unless DISALLOW_ENV_PROXY=1).
func (c *ClaudeSub) clientFor(metadata map[string]any) *http.Client {
	proxy := stringFrom(metadata, "proxy_url")
	if proxy == "" && os.Getenv("DISALLOW_ENV_PROXY") != "1" {
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
			if v := os.Getenv(k); v != "" {
				proxy = v
				break
			}
		}
	}
	if proxy == "" {
		return http.DefaultClient
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cli, ok := c.dispatchers[proxy]; ok {
		return cli
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return http.DefaultClient
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	cli := &http.Client{Transport: tr}
	c.dispatchers[proxy] = cli
	return cli
}

// claudeCloakBody injects billing header + fake user_id metadata when the token
// is an OAuth (sk-ant-oat) token. Personal sk-ant-api03 keys are pass-through.
func claudeCloakBody(body map[string]any, accessToken string) map[string]any {
	out := cloneMapAny(body)
	if !strings.Contains(accessToken, "sk-ant-oat") {
		return out
	}
	billing := map[string]any{"type": "text", "text": claudeBillingHeader(body)}

	switch s := out["system"].(type) {
	case []any:
		// already an array — prepend unless already starts with billing-header
		if len(s) > 0 {
			if m, ok := s[0].(map[string]any); ok {
				if t, _ := m["text"].(string); strings.HasPrefix(t, "x-anthropic-billing-header:") {
					break
				}
			}
		}
		out["system"] = append([]any{billing}, s...)
	case string:
		out["system"] = []any{billing, map[string]any{"type": "text", "text": s}}
	default:
		out["system"] = []any{billing}
	}

	metaAny, _ := out["metadata"].(map[string]any)
	if metaAny == nil {
		metaAny = map[string]any{}
	}
	if _, ok := metaAny["user_id"]; !ok {
		metaAny["user_id"] = fakeUserID()
	}
	out["metadata"] = metaAny
	return out
}

func claudeBillingHeader(body map[string]any) string {
	jb, _ := json.Marshal(body)
	sum := sha256.Sum256(jb)
	cch := hex.EncodeToString(sum[:])[:5]
	build := make([]byte, 2)
	_, _ = rand.Read(build)
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=%s;",
		claudeVersion, hex.EncodeToString(build)[:3], claudeCCEntrypoint, cch)
}

func fakeUserID() string {
	device := make([]byte, 32)
	_, _ = rand.Read(device)
	return fmt.Sprintf(`{"device_id":"%s","account_uuid":"%s","session_id":"%s"}`,
		hex.EncodeToString(device), uuid.NewString(), uuid.NewString())
}

// writeFrames splits the body stream by `\n\n` boundaries and ships each
// completed frame onto out, plus any trailing partial frame at EOF.
func writeFrames(ctx context.Context, body io.Reader, out chan<- []byte) {
	br := bufio.NewReader(body)
	var frame bytes.Buffer
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			frame.Write(line)
			if bytes.HasSuffix(frame.Bytes(), []byte("\n\n")) || bytes.HasSuffix(frame.Bytes(), []byte("\r\n\r\n")) {
				select {
				case out <- append([]byte(nil), frame.Bytes()...):
				case <-ctx.Done():
					return
				}
				frame.Reset()
			}
		}
		if err != nil {
			if frame.Len() > 0 {
				out <- append([]byte(nil), frame.Bytes()...)
			}
			return
		}
	}
}

func cloneMapAny(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringFrom(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}
