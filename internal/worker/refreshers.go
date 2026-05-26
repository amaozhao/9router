package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	claudeTokenURL  = "https://api.anthropic.com/v1/oauth/token"
	claudeClientID  = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	refreshHTTPTimeout = 20 * time.Second
)

// RefreshClaudeOAuth rotates a claude.ai oauth token. claude.ai accepts JSON.
func RefreshClaudeOAuth(ctx context.Context, creds map[string]any, _ RefreshHints) (*RefreshResult, error) {
	rt := strFrom(creds, "refresh_token")
	if rt == "" {
		return nil, fmt.Errorf("missing refresh_token")
	}
	clientID := strFrom(creds, "client_id")
	if clientID == "" {
		clientID = claudeClientID
	}
	body, _ := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": rt,
		"client_id":     clientID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURL, bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	res, err := refreshClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		s := string(buf)
		if len(s) > 200 {
			s = s[:200]
		}
		return nil, fmt.Errorf("refresh %d: %s", res.StatusCode, s)
	}
	var data map[string]any
	if err := json.Unmarshal(buf, &data); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := cloneCreds(creds)
	out["access_token"] = strFrom(data, "access_token")
	if v := strFrom(data, "refresh_token"); v != "" {
		out["refresh_token"] = v
	}
	out["token_endpoint"] = claudeTokenURL
	out["client_id"] = claudeClientID
	expiresIn := numFrom(data, "expires_in")
	if expiresIn == 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	return &RefreshResult{
		Credentials: out,
		ExpiresAt:   &expiresAt,
		Meta: map[string]any{
			"last_refresh_at":     time.Now().UTC().Format(time.RFC3339),
			"last_refresh_status": "ok",
		},
	}, nil
}

// RefreshOAuth2 is a generic refresh_token-flow refresher that reads
// token_endpoint from credentials. Works for OpenAI/Codex, Cursor, GitHub
// Copilot, etc. (anything implementing the OAuth2 refresh flow).
func RefreshOAuth2(ctx context.Context, creds map[string]any, _ RefreshHints) (*RefreshResult, error) {
	rt := strFrom(creds, "refresh_token")
	endpoint := strFrom(creds, "token_endpoint")
	if rt == "" {
		return nil, fmt.Errorf("missing refresh_token")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("missing token_endpoint")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)
	if v := strFrom(creds, "client_id"); v != "" {
		form.Set("client_id", v)
	}
	if v := strFrom(creds, "client_secret"); v != "" {
		form.Set("client_secret", v)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	res, err := refreshClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		s := string(buf)
		if len(s) > 200 {
			s = s[:200]
		}
		return nil, fmt.Errorf("refresh %d: %s", res.StatusCode, s)
	}
	var data map[string]any
	if err := json.Unmarshal(buf, &data); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := cloneCreds(creds)
	out["access_token"] = strFrom(data, "access_token")
	if v := strFrom(data, "refresh_token"); v != "" {
		out["refresh_token"] = v
	}
	expiresIn := numFrom(data, "expires_in")
	if expiresIn == 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	return &RefreshResult{
		Credentials: out,
		ExpiresAt:   &expiresAt,
		Meta: map[string]any{
			"last_refresh_at":     time.Now().UTC().Format(time.RFC3339),
			"last_refresh_status": "ok",
		},
	}, nil
}

// --- helpers ---

func refreshClient() *http.Client { return &http.Client{Timeout: refreshHTTPTimeout} }

func strFrom(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func numFrom(m map[string]any, k string) int64 {
	switch x := m[k].(type) {
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

func cloneCreds(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
