package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/errs"
)

// providerSpec is the static definition of an OAuth provider. One of
// ClientID or ClientIDEnv must be present for the provider to be usable.
type providerSpec struct {
	AuthorizeURL         string
	TokenEndpoint        string
	Scope                string
	ClientID             string
	ClientIDEnv          string
	ClientSecret         string
	ClientSecretEnv      string
	TokenExchangeFormat  string // "json" or "form" (default form)
	AuthorizeExtraParams map[string]string
}

// resolvedProvider is providerSpec with env lookups applied.
type resolvedProvider struct {
	Name                 string
	AuthorizeURL         string
	TokenEndpoint        string
	Scope                string
	ClientID             string
	ClientSecret         string
	TokenExchangeFormat  string
	AuthorizeExtraParams map[string]string
}

func providerSpecs() map[string]providerSpec {
	mockBase := os.Getenv("MOCK_OAUTH_BASE")
	if mockBase == "" {
		mockBase = "http://localhost:31995"
	}
	return map[string]providerSpec{
		"mock": {
			AuthorizeURL:    mockBase + "/authorize",
			TokenEndpoint:   mockBase + "/token",
			Scope:           "mock",
			ClientIDEnv:     "MOCK_OAUTH_CLIENT_ID",
			ClientSecretEnv: "MOCK_OAUTH_CLIENT_SECRET",
		},
		"claude": {
			AuthorizeURL:        "https://claude.ai/oauth/authorize",
			TokenEndpoint:       "https://api.anthropic.com/v1/oauth/token",
			Scope:               "org:create_api_key user:profile user:inference",
			ClientID:            "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
			TokenExchangeFormat: "json",
			AuthorizeExtraParams: map[string]string{
				"code": "true",
			},
		},
		"codex": {
			AuthorizeURL:        "https://auth.openai.com/oauth/authorize",
			TokenEndpoint:       "https://auth.openai.com/oauth/token",
			Scope:               "openid profile email offline_access",
			ClientID:            "app_EMoamEEZ73f0CkXaXp7hrann",
			TokenExchangeFormat: "form",
			AuthorizeExtraParams: map[string]string{
				"id_token_add_organizations": "true",
				"codex_cli_simplified_flow":  "true",
				"originator":                 "codex_cli_rs",
			},
		},
		"openai": {
			AuthorizeURL:    "https://auth.openai.com/authorize",
			TokenEndpoint:   "https://auth.openai.com/oauth/token",
			Scope:           "openid offline_access profile email",
			ClientIDEnv:     "OPENAI_OAUTH_CLIENT_ID",
			ClientSecretEnv: "OPENAI_OAUTH_CLIENT_SECRET",
		},
		"anthropic": {
			AuthorizeURL:    "https://console.anthropic.com/oauth/authorize",
			TokenEndpoint:   "https://console.anthropic.com/v1/oauth/token",
			Scope:           "org:create_api_key user:profile user:inference",
			ClientIDEnv:     "ANTHROPIC_OAUTH_CLIENT_ID",
			ClientSecretEnv: "ANTHROPIC_OAUTH_CLIENT_SECRET",
		},
		"github": {
			AuthorizeURL:    "https://github.com/login/oauth/authorize",
			TokenEndpoint:   "https://github.com/login/oauth/access_token",
			Scope:           "read:user",
			ClientIDEnv:     "GITHUB_OAUTH_CLIENT_ID",
			ClientSecretEnv: "GITHUB_OAUTH_CLIENT_SECRET",
		},
	}
}

func listProviders() []string {
	specs := providerSpecs()
	out := make([]string, 0, len(specs))
	for name := range specs {
		out = append(out, name)
	}
	return out
}

// resolveProvider applies env lookups and reports if the provider is ready.
func resolveProvider(name string) (*resolvedProvider, error) {
	specs := providerSpecs()
	p, ok := specs[name]
	if !ok {
		names := listProviders()
		return nil, errs.Validation(fmt.Sprintf("Unsupported OAuth provider: %s", name),
			map[string]any{"supported": names})
	}
	cid := p.ClientID
	if cid == "" && p.ClientIDEnv != "" {
		cid = os.Getenv(p.ClientIDEnv)
	}
	if cid == "" {
		return nil, errs.Validation(fmt.Sprintf("Provider %s is not configured on this server", name),
			map[string]any{"hint": "set env " + p.ClientIDEnv})
	}
	cs := p.ClientSecret
	if cs == "" && p.ClientSecretEnv != "" {
		cs = os.Getenv(p.ClientSecretEnv)
	}
	fmtt := p.TokenExchangeFormat
	if fmtt == "" {
		fmtt = "form"
	}
	return &resolvedProvider{
		Name:                 name,
		AuthorizeURL:         p.AuthorizeURL,
		TokenEndpoint:        p.TokenEndpoint,
		Scope:                p.Scope,
		ClientID:             cid,
		ClientSecret:         cs,
		TokenExchangeFormat:  fmtt,
		AuthorizeExtraParams: p.AuthorizeExtraParams,
	}, nil
}

// buildPKCE creates a verifier + S256 challenge (base64url, no padding).
func buildPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func buildAuthorizeURL(p *resolvedProvider, state, challenge, redirectURI string) (string, error) {
	u, err := url.Parse(p.AuthorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", p.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	for k, v := range p.AuthorizeExtraParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// exchangeCode posts code + verifier to the token endpoint and returns the
// parsed response map. Tolerates form-encoded responses (GitHub).
func exchangeCode(ctx context.Context, p *resolvedProvider, code, verifier, redirectURI, state string) (map[string]any, error) {
	// claude.ai mangles state into the code as `code#state`; strip it.
	authCode := code
	if i := strings.Index(authCode, "#"); i >= 0 {
		authCode = code[:i]
	}

	var body []byte
	var ctype string
	if p.TokenExchangeFormat == "json" {
		payload := map[string]any{
			"grant_type":    "authorization_code",
			"code":          authCode,
			"state":         state,
			"redirect_uri":  redirectURI,
			"client_id":     p.ClientID,
			"code_verifier": verifier,
		}
		if p.ClientSecret != "" {
			payload["client_secret"] = p.ClientSecret
		}
		body, _ = json.Marshal(payload)
		ctype = "application/json"
	} else {
		v := url.Values{}
		v.Set("grant_type", "authorization_code")
		v.Set("code", authCode)
		v.Set("redirect_uri", redirectURI)
		v.Set("client_id", p.ClientID)
		v.Set("code_verifier", verifier)
		if p.ClientSecret != "" {
			v.Set("client_secret", p.ClientSecret)
		}
		body = []byte(v.Encode())
		ctype = "application/x-www-form-urlencoded"
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, bytes.NewReader(body))
	req.Header.Set("content-type", ctype)
	req.Header.Set("accept", "application/json")

	cli := &http.Client{Timeout: 20 * time.Second}
	res, err := cli.Do(req)
	if err != nil {
		return nil, errs.Upstream("token exchange transport failed: " + err.Error())
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		snippet := string(buf)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return nil, errs.Upstream(fmt.Sprintf("Token exchange failed (%d): %s", res.StatusCode, snippet),
			map[string]any{"status_code": res.StatusCode})
	}
	var data map[string]any
	if err := json.Unmarshal(buf, &data); err == nil {
		return data, nil
	}
	// Fallback: form-encoded (GitHub).
	v, ferr := url.ParseQuery(string(buf))
	if ferr != nil {
		return nil, errs.Upstream("token response is neither JSON nor form-encoded",
			map[string]any{"body": string(buf)})
	}
	out := make(map[string]any, len(v))
	for k := range v {
		out[k] = v.Get(k)
	}
	return out, nil
}
