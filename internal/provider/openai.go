// Package provider hosts upstream-specific clients. The OpenAI-compatible
// client speaks the Chat Completions API and any vendor that mimics it
// (Gemini OpenAI shim, GLM, DeepSeek, MiniMax, OpenRouter, Together, …).
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/amaozhao/lazirouter/internal/errs"
)

// OpenAI is a slim stateful client; one per process is fine.
type OpenAI struct {
	HTTP *http.Client
}

func NewOpenAI() *OpenAI {
	return &OpenAI{HTTP: &http.Client{}}
}

// ChatCompletion makes a non-stream POST {baseURL}/v1/chat/completions.
// `body` is forwarded verbatim with stream forced to false.
func (o *OpenAI) ChatCompletion(ctx context.Context, baseURL, apiKey string, body map[string]any) (map[string]any, error) {
	body["stream"] = false
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, trimSlash(baseURL)+"/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("authorization", "Bearer "+apiKey)
	req.Header.Set("content-type", "application/json")
	res, err := o.HTTP.Do(req)
	if err != nil {
		return nil, errs.Upstream("upstream call failed: " + err.Error())
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, errs.Upstream(fmt.Sprintf("Upstream %d: %s", res.StatusCode, snippet(buf)),
			map[string]any{"status_code": res.StatusCode, "body": string(buf)})
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, errs.Upstream("Upstream returned non-JSON", map[string]any{"body": snippet(buf)})
	}
	return out, nil
}

// StreamChatCompletion returns a channel of raw SSE frames (each terminated by
// "\n\n"). The channel closes when upstream EOFs or ctx is cancelled. Errors
// are sent on errCh before the data channel closes.
func (o *OpenAI) StreamChatCompletion(ctx context.Context, baseURL, apiKey string, body map[string]any) (<-chan []byte, <-chan error) {
	body["stream"] = true
	data := make(chan []byte, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(data)
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, trimSlash(baseURL)+"/v1/chat/completions", bytes.NewReader(raw))
		req.Header.Set("authorization", "Bearer "+apiKey)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "text/event-stream")
		res, err := o.HTTP.Do(req)
		if err != nil {
			errs <- upstreamErr(0, []byte(err.Error()))
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			buf, _ := io.ReadAll(res.Body)
			errs <- upstreamErr(res.StatusCode, buf)
			return
		}
		// SSE frames separated by blank lines (\n\n). bufio.Scanner with a custom
		// SplitFunc keeps the trailing separator in the returned slice.
		br := bufio.NewReader(res.Body)
		var frame bytes.Buffer
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				frame.Write(line)
				if bytes.HasSuffix(frame.Bytes(), []byte("\n\n")) || bytes.HasSuffix(frame.Bytes(), []byte("\r\n\r\n")) {
					select {
					case data <- append([]byte(nil), frame.Bytes()...):
					case <-ctx.Done():
						return
					}
					frame.Reset()
				}
			}
			if err != nil {
				if frame.Len() > 0 {
					data <- append([]byte(nil), frame.Bytes()...)
				}
				if err != io.EOF {
					errs <- err
				}
				return
			}
		}
	}()
	return data, errs
}

// Embed posts to /v1/embeddings.
func (o *OpenAI) Embed(ctx context.Context, baseURL, apiKey string, body map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, trimSlash(baseURL)+"/v1/embeddings", bytes.NewReader(raw))
	req.Header.Set("authorization", "Bearer "+apiKey)
	req.Header.Set("content-type", "application/json")
	res, err := o.HTTP.Do(req)
	if err != nil {
		return nil, errs.Upstream("upstream call failed: " + err.Error())
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, errs.Upstream(fmt.Sprintf("Upstream %d: %s", res.StatusCode, snippet(buf)),
			map[string]any{"status_code": res.StatusCode, "body": string(buf)})
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, errs.Upstream("Upstream returned non-JSON", map[string]any{"body": snippet(buf)})
	}
	return out, nil
}

// HTTPWithProxy returns an *http.Client whose Transport honours a per-connection
// proxy URL (HTTP(S)). Pass an empty string to get the default client.
func HTTPWithProxy(proxyURL string) *http.Client {
	if proxyURL == "" {
		return http.DefaultClient
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return http.DefaultClient
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: tr}
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }
func snippet(b []byte) string {
	if len(b) > 500 {
		return string(b[:500])
	}
	return string(b)
}
func upstreamErr(code int, body []byte) error {
	if code == 0 {
		return errs.Upstream("upstream stream failed: " + string(body))
	}
	return errs.Upstream(fmt.Sprintf("Upstream %d: %s", code, snippet(body)),
		map[string]any{"status_code": code, "body": string(body)})
}
