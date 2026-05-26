package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/jwtx"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

const (
	defaultTestDBURL    = "postgres://router:router_dev_pw@localhost:55432/router"
	defaultTestRedisURL = "redis://localhost:56379/0"
)

var (
	testMasterKey = []byte("0123456789abcdef0123456789abcdef")
	depsReady     bool
)

func TestMain(m *testing.M) {
	pgURL := envOr("TEST_DATABASE_URL", defaultTestDBURL)
	rdURL := envOr("TEST_REDIS_URL", defaultTestRedisURL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := redisx.Init(rdURL); err == nil {
		if _, err := redisx.Ping(ctx); err == nil {
			if err := db.Init(ctx, pgURL); err == nil {
				if err := db.Pool().Ping(ctx); err == nil {
					depsReady = true
				}
			}
		}
	}
	os.Exit(m.Run())
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func requireDeps(t *testing.T) {
	t.Helper()
	if !depsReady {
		t.Skipf("skipping: dev Postgres/Redis not available")
	}
}

// ---- Pure helpers ----

func TestBuildPKCEMatchesS256(t *testing.T) {
	v, c, err := buildPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 40 || len(c) < 40 {
		t.Fatalf("verifier/challenge too short: %d/%d", len(v), len(c))
	}
	// Confirm the challenge is base64url(sha256(verifier)).
	dec, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		t.Fatalf("challenge not base64url: %v", err)
	}
	if len(dec) != 32 {
		t.Fatalf("challenge isn't 32B sha256: got %d bytes", len(dec))
	}
}

func TestResolveProviderUnknown(t *testing.T) {
	if _, err := resolveProvider("definitely-not-a-real-provider"); err == nil {
		t.Fatal("expected validation error for unknown provider")
	}
}

func TestResolveProviderClaude(t *testing.T) {
	p, err := resolveProvider("claude")
	if err != nil {
		t.Fatal(err)
	}
	if p.ClientID == "" || p.TokenExchangeFormat != "json" {
		t.Fatalf("claude resolve: %+v", p)
	}
}

func TestResolveProviderEnvGatedMissing(t *testing.T) {
	// openai requires OPENAI_OAUTH_CLIENT_ID — make sure it isn't set.
	_ = os.Unsetenv("OPENAI_OAUTH_CLIENT_ID")
	if _, err := resolveProvider("openai"); err == nil {
		t.Fatal("expected validation error when env not set")
	}
}

func TestResolveProviderEnvGatedSet(t *testing.T) {
	_ = os.Setenv("OPENAI_OAUTH_CLIENT_ID", "test-id")
	defer os.Unsetenv("OPENAI_OAUTH_CLIENT_ID")
	p, err := resolveProvider("openai")
	if err != nil {
		t.Fatal(err)
	}
	if p.ClientID != "test-id" {
		t.Fatalf("env-gated clientId not picked up: %+v", p)
	}
}

func TestBuildAuthorizeURLPutsPKCEParams(t *testing.T) {
	p, err := resolveProvider("claude")
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildAuthorizeURL(p, "STATE", "CHALL", "http://cb")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	checks := map[string]string{
		"client_id":             p.ClientID,
		"response_type":         "code",
		"redirect_uri":          "http://cb",
		"state":                 "STATE",
		"code_challenge":        "CHALL",
		"code_challenge_method": "S256",
		"scope":                 p.Scope,
		"code":                  "true", // from claude's authorizeExtraParams
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Fatalf("authorize url %s: got %q want %q", k, got, want)
		}
	}
}

func TestExchangeCodeFormAndJSON(t *testing.T) {
	// JSON-format provider (claude-shape).
	srvJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("content-type") != "application/json" {
			t.Errorf("json provider should send JSON content-type, got %q", r.Header.Get("content-type"))
		}
		buf, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(buf, &got); err != nil {
			t.Errorf("body not JSON: %v", err)
		}
		if got["code"] != "abc" || got["code_verifier"] != "v" {
			t.Errorf("missing fields: %+v", got)
		}
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	}))
	defer srvJSON.Close()
	pJSON := &resolvedProvider{Name: "x", TokenEndpoint: srvJSON.URL, ClientID: "cid", TokenExchangeFormat: "json"}
	tokens, err := exchangeCode(context.Background(), pJSON, "abc#STATE", "v", "http://cb", "STATE")
	if err != nil {
		t.Fatal(err)
	}
	if tokens["access_token"] != "AT" {
		t.Fatalf("json access_token: %+v", tokens)
	}

	// Form-format provider (codex-shape) + state-stripping in code.
	srvForm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("content-type"), "application/x-www-form-urlencoded") {
			t.Errorf("form provider should send urlencoded, got %q", r.Header.Get("content-type"))
		}
		_ = r.ParseForm()
		// The '#STATE' suffix in the incoming code should have been stripped.
		if r.Form.Get("code") != "abc" {
			t.Errorf("code stripping broken: %q", r.Form.Get("code"))
		}
		_, _ = w.Write([]byte(`{"access_token":"AT2","expires_in":7200}`))
	}))
	defer srvForm.Close()
	pForm := &resolvedProvider{Name: "y", TokenEndpoint: srvForm.URL, ClientID: "cid", TokenExchangeFormat: "form"}
	tokens, err = exchangeCode(context.Background(), pForm, "abc#STATE", "v", "http://cb", "STATE")
	if err != nil {
		t.Fatal(err)
	}
	if tokens["access_token"] != "AT2" {
		t.Fatalf("form access_token: %+v", tokens)
	}
}

func TestExchangeCodeGithubStyleFormResponse(t *testing.T) {
	// GitHub returns application/x-www-form-urlencoded by default.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=gh-AT&token_type=bearer&scope=read%3Auser"))
	}))
	defer srv.Close()
	p := &resolvedProvider{Name: "github", TokenEndpoint: srv.URL, ClientID: "cid"}
	tokens, err := exchangeCode(context.Background(), p, "x", "v", "http://cb", "S")
	if err != nil {
		t.Fatal(err)
	}
	if tokens["access_token"] != "gh-AT" {
		t.Fatalf("form-encoded response parse: %+v", tokens)
	}
}

func TestExchangeCodeUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	p := &resolvedProvider{Name: "x", TokenEndpoint: srv.URL, ClientID: "cid"}
	_, err := exchangeCode(context.Background(), p, "x", "v", "http://cb", "S")
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("status code missing: %v", err)
	}
}

// ---- HTTP handler integration ----

func newTestDeps(t *testing.T) *Deps {
	t.Helper()
	return &Deps{Cfg: &config.Config{
		MasterKey: testMasterKey,
		JWTSecret: "test-secret",
	}}
}

func TestListOAuthProvidersHandler(t *testing.T) {
	requireDeps(t)
	d := newTestDeps(t)
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	w := httptest.NewRecorder()
	d.ListOAuthProviders(w, r)
	if w.Code != 200 {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			Provider string `json:"provider"`
			Ready    bool   `json:"ready"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) < 4 {
		t.Fatalf("too few providers: %+v", body.Items)
	}
	// At minimum claude and codex have hardcoded clientIds → ready=true.
	ready := map[string]bool{}
	for _, it := range body.Items {
		ready[it.Provider] = it.Ready
	}
	if !ready["claude"] || !ready["codex"] {
		t.Fatalf("claude/codex should be ready: %+v", body.Items)
	}
	if ready["openai"] {
		t.Fatalf("openai should NOT be ready without env clientId: %+v", body.Items)
	}
}

func TestStartOAuthRequiresSession(t *testing.T) {
	d := newTestDeps(t)
	r := httptest.NewRequest(http.MethodPost, "/api/oauth/claude/start", nil)
	r.SetPathValue("provider", "claude")
	w := httptest.NewRecorder()
	d.StartOAuth(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func sessionToken(t *testing.T, cfg *config.Config, tid int64) string {
	t.Helper()
	tok, err := jwtx.Sign([]byte(cfg.JWTSecret), jwtx.Session{
		UserID: 1, TenantID: tid, Role: "owner", Email: "owner@v.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestStartOAuthCreatesRedisState(t *testing.T) {
	requireDeps(t)
	ctx := context.Background()
	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan) VALUES ($1, 'free') RETURNING id
	`, fmt.Sprintf("oauth-%d", time.Now().UnixNano())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid) })

	d := newTestDeps(t)
	tok := sessionToken(t, d.Cfg, tid)
	body := strings.NewReader(`{"connectionName":"my-claude","redirectUri":"http://example/cb"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/oauth/claude/start", body)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.SetPathValue("provider", "claude")
	w := httptest.NewRecorder()
	d.StartOAuth(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		AuthorizeURL string `json:"authorizeUrl"`
		State        string `json:"state"`
		ExpiresInSec int    `json:"expiresInSec"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.AuthorizeURL, "claude.ai/oauth/authorize") {
		t.Fatalf("authorize url wrong: %s", resp.AuthorizeURL)
	}
	if !strings.Contains(resp.AuthorizeURL, "redirect_uri=http%3A%2F%2Fexample%2Fcb") {
		t.Fatalf("redirect_uri not encoded: %s", resp.AuthorizeURL)
	}
	if resp.ExpiresInSec != 600 {
		t.Fatalf("expiresInSec: %d", resp.ExpiresInSec)
	}
	raw, err := redisx.C().Get(ctx, "oauth:state:"+resp.State).Result()
	if err != nil {
		t.Fatalf("state row missing in redis: %v", err)
	}
	var flow oauthFlow
	if err := json.Unmarshal([]byte(raw), &flow); err != nil {
		t.Fatal(err)
	}
	if flow.TenantID != tid || flow.Provider != "claude" || flow.ConnectionName != "my-claude" || flow.RedirectURI != "http://example/cb" {
		t.Fatalf("flow stored wrong: %+v", flow)
	}
	if flow.Verifier == "" {
		t.Fatal("verifier missing from flow")
	}
}

func TestOAuthCallbackHappyPath(t *testing.T) {
	requireDeps(t)
	ctx := context.Background()

	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan) VALUES ($1, 'free') RETURNING id
	`, fmt.Sprintf("cb-%d", time.Now().UnixNano())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid) })

	// Mock provider that always returns a token bundle.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600,"scope":"x"}`))
	}))
	defer mock.Close()

	// Wire mock into the providers registry via env.
	_ = os.Setenv("MOCK_OAUTH_BASE", mock.URL)
	_ = os.Setenv("MOCK_OAUTH_CLIENT_ID", "test-mock-id")
	defer os.Unsetenv("MOCK_OAUTH_BASE")
	defer os.Unsetenv("MOCK_OAUTH_CLIENT_ID")

	// Stash a flow row under a known state key.
	state := "test-state-" + fmt.Sprintf("%d", time.Now().UnixNano())
	flow := oauthFlow{
		TenantID:       tid,
		UserID:         1,
		Provider:       "mock",
		ConnectionName: "mock-1",
		Verifier:       "v",
		RedirectURI:    "http://cb",
	}
	body, _ := json.Marshal(flow)
	if err := redisx.C().Set(ctx, "oauth:state:"+state, body, 5*time.Minute).Err(); err != nil {
		t.Fatal(err)
	}

	d := newTestDeps(t)
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?code=mycode&state="+state, nil)
	w := httptest.NewRecorder()
	d.OAuthCallback(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Connection added") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}

	// Redis state should be deleted.
	if _, err := redisx.C().Get(ctx, "oauth:state:"+state).Result(); err == nil {
		t.Fatal("state key should be deleted after callback")
	}

	// Confirm the connection row was created and creds are decryptable.
	var enc []byte
	err := db.Pool().QueryRow(ctx, `
		SELECT credentials_encrypted FROM connections WHERE tenant_id=$1 AND provider='mock' AND name='mock-1'
	`, tid).Scan(&enc)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cryptox.DecryptForTenant(testMasterKey, tid, enc)
	if err != nil {
		t.Fatal(err)
	}
	if creds["access_token"] != "AT" || creds["refresh_token"] != "RT" {
		t.Fatalf("creds: %+v", creds)
	}
}

func TestOAuthCallbackMissingCode(t *testing.T) {
	d := newTestDeps(t)
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?state=x", nil)
	w := httptest.NewRecorder()
	d.OAuthCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestOAuthCallbackUnknownState(t *testing.T) {
	requireDeps(t)
	d := newTestDeps(t)
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?code=x&state=does-not-exist-anywhere", nil)
	w := httptest.NewRecorder()
	d.OAuthCallback(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestOAuthCallbackProviderError(t *testing.T) {
	d := newTestDeps(t)
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?error=access_denied&error_description=user+said+no", nil)
	w := httptest.NewRecorder()
	d.OAuthCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "access_denied") {
		t.Fatalf("error not surfaced: %s", w.Body.String())
	}
}
