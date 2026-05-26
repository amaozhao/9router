package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
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

func TestStrFrom(t *testing.T) {
	m := map[string]any{"s": "hi", "n": 5, "nilv": nil}
	if got := strFrom(m, "s"); got != "hi" {
		t.Fatalf("hit: %q", got)
	}
	if got := strFrom(m, "n"); got != "" {
		t.Fatalf("non-string: %q", got)
	}
	if got := strFrom(m, "missing"); got != "" {
		t.Fatalf("missing: %q", got)
	}
	if got := strFrom(m, "nilv"); got != "" {
		t.Fatalf("nil: %q", got)
	}
}

func TestNumFrom(t *testing.T) {
	m := map[string]any{
		"f64":     float64(3600),
		"i":       42,
		"i64":     int64(7),
		"jsonnum": json.Number("9"),
		"str":     "ignored",
	}
	for k, want := range map[string]int64{"f64": 3600, "i": 42, "i64": 7, "jsonnum": 9, "str": 0, "missing": 0} {
		if got := numFrom(m, k); got != want {
			t.Fatalf("numFrom(%q): got %d want %d", k, got, want)
		}
	}
}

func TestCloneCreds(t *testing.T) {
	src := map[string]any{"a": 1, "b": "x"}
	dst := cloneCreds(src)
	dst["a"] = 999
	if src["a"] != 1 {
		t.Fatalf("source mutated: %+v", src)
	}
	if dst["b"] != "x" {
		t.Fatalf("dst missing key: %+v", dst)
	}
}

func TestRegisterLookup(t *testing.T) {
	called := false
	fn := func(ctx context.Context, _ map[string]any, _ RefreshHints) (*RefreshResult, error) {
		called = true
		return &RefreshResult{Credentials: map[string]any{}}, nil
	}
	Register("test-provider-unique", fn)
	got := lookup("test-provider-unique")
	if got == nil {
		t.Fatal("expected fn back")
	}
	_, _ = got(context.Background(), nil, RefreshHints{})
	if !called {
		t.Fatal("registered fn was not the one returned")
	}
	if lookup("definitely-not-registered") != nil {
		t.Fatal("nil expected for missing provider")
	}
}

// ---- HTTP-driven OAuth refreshers ----

func TestRefreshClaudeOAuth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method: %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"access_token":"new-A","refresh_token":"new-R","expires_in":7200}`))
	}))
	defer srv.Close()
	// Point claudeTokenURL at our test server by reflection-free swap: we call
	// RefreshOAuth2 which respects token_endpoint instead.
	creds := map[string]any{
		"refresh_token":  "old",
		"token_endpoint": srv.URL,
	}
	res, err := RefreshOAuth2(context.Background(), creds, RefreshHints{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Credentials["access_token"] != "new-A" {
		t.Fatalf("access_token not rotated: %+v", res.Credentials)
	}
	if res.Credentials["refresh_token"] != "new-R" {
		t.Fatalf("refresh_token not rotated: %+v", res.Credentials)
	}
	if res.ExpiresAt == nil {
		t.Fatal("expires_at not set")
	}
	delta := time.Until(*res.ExpiresAt).Seconds()
	if delta < 7100 || delta > 7300 {
		t.Fatalf("expiry off: %.0fs", delta)
	}
}

func TestRefreshOAuth2_MissingFields(t *testing.T) {
	if _, err := RefreshOAuth2(context.Background(), map[string]any{}, RefreshHints{}); err == nil {
		t.Fatal("expected error for missing refresh_token")
	}
	if _, err := RefreshOAuth2(context.Background(), map[string]any{"refresh_token": "x"}, RefreshHints{}); err == nil {
		t.Fatal("expected error for missing token_endpoint")
	}
}

func TestRefreshOAuth2_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	_, err := RefreshOAuth2(context.Background(), map[string]any{
		"refresh_token": "x", "token_endpoint": srv.URL,
	}, RefreshHints{})
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("missing status in error: %v", err)
	}
}

// ---- DB-driven aggregator ----

func TestAggregateOnce(t *testing.T) {
	requireDeps(t)
	ctx := context.Background()

	// Make an isolated tenant with two usage_events in the same hour bucket.
	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan) VALUES ($1, 'free') RETURNING id
	`, fmt.Sprintf("agg-%d", time.Now().UnixNano())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid) })
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM usage_events WHERE tenant_id = $1`, tid)
		_, _ = db.Pool().Exec(ctx, `DELETE FROM usage_summaries WHERE tenant_id = $1`, tid)
	})

	for i := 0; i < 3; i++ {
		_, err := db.Pool().Exec(ctx, `
			INSERT INTO usage_events
			  (tenant_id, ts, provider, model, prompt_tokens, completion_tokens, total_tokens, cost_micros, status)
			VALUES ($1, now(), 'openai', 'gpt-4o-mini', 10, 20, 30, 100, 'ok')
		`, tid)
		if err != nil {
			t.Fatal(err)
		}
	}
	// one errored row
	_, err := db.Pool().Exec(ctx, `
		INSERT INTO usage_events
		  (tenant_id, ts, provider, model, prompt_tokens, completion_tokens, total_tokens, cost_micros, status)
		VALUES ($1, now(), 'openai', 'gpt-4o-mini', 5, 5, 10, 50, 'error')
	`, tid)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := AggregateOnce(ctx); err != nil {
		t.Fatalf("AggregateOnce: %v", err)
	}

	var (
		reqCount   int64
		promptSum  int64
		errorCount int64
	)
	err = db.Pool().QueryRow(ctx, `
		SELECT request_count, prompt_tokens, error_count
		FROM usage_summaries
		WHERE tenant_id = $1 AND provider = 'openai' AND model = 'gpt-4o-mini'
	`, tid).Scan(&reqCount, &promptSum, &errorCount)
	if err != nil {
		t.Fatalf("summary not written: %v", err)
	}
	if reqCount != 4 {
		t.Fatalf("request_count: got %d want 4", reqCount)
	}
	if promptSum != 35 {
		t.Fatalf("prompt_tokens: got %d want 35", promptSum)
	}
	if errorCount != 1 {
		t.Fatalf("error_count: got %d want 1", errorCount)
	}
}

// ---- RefreshOnce end-to-end ----

func TestRefreshOnce_RotatesCredentials(t *testing.T) {
	requireDeps(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"rotated","refresh_token":"new-rt","expires_in":3600}`))
	}))
	defer srv.Close()

	const provider = "test-rotate"
	Register(provider, RefreshOAuth2)

	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan) VALUES ($1, 'free') RETURNING id
	`, fmt.Sprintf("rot-%d", time.Now().UnixNano())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid) })

	creds := map[string]any{
		"refresh_token":  "old-rt",
		"token_endpoint": srv.URL,
		"access_token":   "old-at",
	}
	enc, err := cryptox.EncryptForTenant(testMasterKey, tid, creds)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(10 * time.Second) // expiring soon → eligible
	var connID int64
	err = db.Pool().QueryRow(ctx, `
		INSERT INTO connections
		  (tenant_id, provider, name, auth_type, credentials_encrypted, oauth_expires_at, enabled)
		VALUES ($1, $2, 'c1', 'oauth', $3, $4, TRUE) RETURNING id
	`, tid, provider, enc, expires).Scan(&connID)
	if err != nil {
		t.Fatal(err)
	}

	// Clean any stale lock.
	_ = redisx.C().Del(ctx, fmt.Sprintf("lock:refresh:%d", connID)).Err()

	n, err := RefreshOnce(ctx, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected ≥1 refresh, got %d", n)
	}

	// Confirm DB row now has the rotated access_token.
	var encOut []byte
	var newExpires *time.Time
	err = db.Pool().QueryRow(ctx, `
		SELECT credentials_encrypted, oauth_expires_at FROM connections WHERE id = $1
	`, connID).Scan(&encOut, &newExpires)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := cryptox.DecryptForTenant(testMasterKey, tid, encOut)
	if err != nil {
		t.Fatal(err)
	}
	if rotated["access_token"] != "rotated" {
		t.Fatalf("access_token not rotated: %+v", rotated)
	}
	if rotated["refresh_token"] != "new-rt" {
		t.Fatalf("refresh_token not rotated: %+v", rotated)
	}
	if newExpires == nil || time.Until(*newExpires) < 50*time.Minute {
		t.Fatalf("expires_at not pushed forward: %v", newExpires)
	}
}

func TestRefreshOnce_SETNXLockBlocksConcurrentRun(t *testing.T) {
	requireDeps(t)
	ctx := context.Background()

	// One connection that expires soon, refresher counts how many times it's
	// called across two concurrent goroutines.
	const provider = "test-lock"
	var calls int
	Register(provider, func(_ context.Context, c map[string]any, _ RefreshHints) (*RefreshResult, error) {
		calls++
		// Simulate a slow upstream so the second runner has time to hit the lock.
		time.Sleep(150 * time.Millisecond)
		out := cloneCreds(c)
		out["access_token"] = "x"
		expires := time.Now().Add(time.Hour)
		return &RefreshResult{Credentials: out, ExpiresAt: &expires}, nil
	})

	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan) VALUES ($1, 'free') RETURNING id
	`, fmt.Sprintf("lock-%d", time.Now().UnixNano())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid) })

	creds := map[string]any{"refresh_token": "x"}
	enc, _ := cryptox.EncryptForTenant(testMasterKey, tid, creds)
	expires := time.Now().Add(5 * time.Second)
	var connID int64
	_ = db.Pool().QueryRow(ctx, `
		INSERT INTO connections
		  (tenant_id, provider, name, auth_type, credentials_encrypted, oauth_expires_at, enabled)
		VALUES ($1, $2, 'c1', 'oauth', $3, $4, TRUE) RETURNING id
	`, tid, provider, enc, expires).Scan(&connID)

	_ = redisx.C().Del(ctx, fmt.Sprintf("lock:refresh:%d", connID)).Err()

	done := make(chan struct{}, 2)
	go func() { _, _ = RefreshOnce(ctx, testMasterKey); done <- struct{}{} }()
	go func() { _, _ = RefreshOnce(ctx, testMasterKey); done <- struct{}{} }()
	<-done
	<-done

	if calls != 1 {
		t.Fatalf("SETNX lock should have admitted exactly 1 refresh, got %d", calls)
	}
}
