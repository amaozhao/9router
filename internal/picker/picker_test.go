package picker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

const (
	defaultTestDBURL    = "postgres://router:router_dev_pw@localhost:55432/router"
	defaultTestRedisURL = "redis://localhost:56379/0"
)

var (
	testMasterKey = []byte("0123456789abcdef0123456789abcdef") // 32B
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

// ---- Pure helpers ----

func TestMax1(t *testing.T) {
	cases := []struct{ in, want int }{
		{-5, 1}, {0, 1}, {1, 1}, {3, 3}, {99, 99},
	}
	for _, c := range cases {
		if got := max1(c.in); got != c.want {
			t.Fatalf("max1(%d): got %d want %d", c.in, got, c.want)
		}
	}
}

func TestBase64RoundTrip(t *testing.T) {
	in := []byte{0, 1, 2, 0xfe, 0xff, 0x80}
	enc := base64StdEncode(in)
	if enc != base64.StdEncoding.EncodeToString(in) {
		t.Fatalf("encoder drifted from std")
	}
	dec, err := base64StdDecode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != string(in) {
		t.Fatalf("round-trip mismatch: got %x want %x", dec, in)
	}
	if _, err := base64StdDecode("@@@"); err == nil {
		t.Fatal("expected decode error on bad input")
	}
}

func TestKeyHelpers(t *testing.T) {
	if got, want := cursorKey(7, "openai"), "account_cursor:7:openai"; got != want {
		t.Fatalf("cursorKey: %q vs %q", got, want)
	}
	if got, want := cooldownKey(7, "openai", 42), "cooldown:7:openai:42"; got != want {
		t.Fatalf("cooldownKey: %q vs %q", got, want)
	}
	if got, want := connCacheKey(7, "openai"), "conn_cache:7:openai"; got != want {
		t.Fatalf("connCacheKey: %q vs %q", got, want)
	}
}

// ---- Integration (live Redis + Postgres) ----

func requireDeps(t *testing.T) {
	t.Helper()
	if !depsReady {
		t.Skipf("skipping: dev Postgres/Redis not available")
	}
}

func newTenant(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	var tid int64
	err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan, status, meta)
		VALUES ($1, 'free', 'active', '{}'::jsonb) RETURNING id
	`, fmt.Sprintf("test-%d", time.Now().UnixNano())).Scan(&tid)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
	})
	return tid
}

func newConnection(t *testing.T, tenantID int64, provider, name string, weight int, creds map[string]any) int64 {
	t.Helper()
	enc, err := cryptox.EncryptForTenant(testMasterKey, tenantID, creds)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	var id int64
	err = db.Pool().QueryRow(context.Background(), `
		INSERT INTO connections (tenant_id, provider, name, auth_type, credentials_encrypted, weight)
		VALUES ($1,$2,$3,'api_key',$4,$5) RETURNING id
	`, tenantID, provider, name, enc, weight).Scan(&id)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	return id
}

func flushPickerKeys(t *testing.T, tenantID int64, provider string) {
	t.Helper()
	ctx := context.Background()
	keys := []string{
		connCacheKey(tenantID, provider),
		cursorKey(tenantID, provider),
	}
	if err := redisx.C().Del(ctx, keys...).Err(); err != nil {
		t.Fatalf("flush keys: %v", err)
	}
	// also nuke any cooldown:* / strikes:* for this tenant+provider
	iter := redisx.C().Scan(ctx, 0, fmt.Sprintf("cooldown*:%d:%s:*", tenantID, provider), 100).Iterator()
	for iter.Next(ctx) {
		_ = redisx.C().Del(ctx, iter.Val()).Err()
	}
}

func TestPickWeightedRR(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	a := newConnection(t, tid, "openai", "a", 1, map[string]any{"api_key": "k-a"})
	b := newConnection(t, tid, "openai", "b", 3, map[string]any{"api_key": "k-b"})
	flushPickerKeys(t, tid, "openai")

	hits := map[int64]int{}
	for i := 0; i < 80; i++ {
		acc, err := Pick(context.Background(), testMasterKey, tid, "openai")
		if err != nil {
			t.Fatalf("Pick #%d: %v", i, err)
		}
		hits[acc.ConnectionID]++
	}
	// With weights 1:3 over 80 picks we expect ~20 vs ~60.
	if hits[a] == 0 || hits[b] == 0 {
		t.Fatalf("expected hits on both, got %+v", hits)
	}
	if hits[b] <= hits[a] {
		t.Fatalf("expected higher-weight connection to dominate: a=%d b=%d", hits[a], hits[b])
	}
}

func TestPickReturnsDecryptedCreds(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	creds := map[string]any{"api_key": "sk-secret", "extra": "v"}
	_ = newConnection(t, tid, "glm", "only", 1, creds)
	flushPickerKeys(t, tid, "glm")

	acc, err := Pick(context.Background(), testMasterKey, tid, "glm")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Credentials["api_key"] != "sk-secret" || acc.Credentials["extra"] != "v" {
		t.Fatalf("creds not round-tripped: %+v", acc.Credentials)
	}
}

func TestPickNoConnections(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	flushPickerKeys(t, tid, "openai")
	_, err := Pick(context.Background(), testMasterKey, tid, "openai")
	if err == nil {
		t.Fatal("expected NoAccountAvailable, got nil")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "no_account_available" {
		t.Fatalf("wrong code: %v", err)
	}
}

func TestPickAllInCooldown(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	id := newConnection(t, tid, "openai", "only", 1, map[string]any{"api_key": "k"})
	flushPickerKeys(t, tid, "openai")

	if _, err := MarkCooldown(context.Background(), tid, "openai", id); err != nil {
		t.Fatal(err)
	}
	_, err := Pick(context.Background(), testMasterKey, tid, "openai")
	if err == nil {
		t.Fatal("expected NoAccountAvailable when only connection is in cooldown")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "no_account_available" {
		t.Fatalf("wrong code: %v", err)
	}
}

func TestMarkCooldownExponentialBackoff(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	id := newConnection(t, tid, "deepseek", "x", 1, map[string]any{"api_key": "k"})
	flushPickerKeys(t, tid, "deepseek")
	t.Cleanup(func() { _ = ClearCooldown(context.Background(), tid, "deepseek", id) })

	// 60s, 120s, 240s, 480s ...
	expect := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second}
	for i, want := range expect {
		got, err := MarkCooldown(context.Background(), tid, "deepseek", id)
		if err != nil {
			t.Fatalf("strike %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("strike %d duration: got %s want %s", i+1, got, want)
		}
	}
}

func TestClearCooldown(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	id := newConnection(t, tid, "openai", "y", 1, map[string]any{"api_key": "k"})
	flushPickerKeys(t, tid, "openai")

	if _, err := MarkCooldown(context.Background(), tid, "openai", id); err != nil {
		t.Fatal(err)
	}
	if err := ClearCooldown(context.Background(), tid, "openai", id); err != nil {
		t.Fatal(err)
	}
	// After clearing, Pick should succeed.
	if _, err := Pick(context.Background(), testMasterKey, tid, "openai"); err != nil {
		t.Fatalf("expected pick after clear, got %v", err)
	}
}

func TestInvalidateCacheAfterInsert(t *testing.T) {
	requireDeps(t)
	tid := newTenant(t)
	flushPickerKeys(t, tid, "openai")
	_ = newConnection(t, tid, "openai", "first", 1, map[string]any{"api_key": "k1"})
	// Prime cache.
	if _, err := Pick(context.Background(), testMasterKey, tid, "openai"); err != nil {
		t.Fatal(err)
	}
	// Add a second connection; without InvalidateCache the picker would never
	// see it within the 5s cache window.
	id2 := newConnection(t, tid, "openai", "second", 1, map[string]any{"api_key": "k2"})
	if err := InvalidateCache(context.Background(), tid, "openai"); err != nil {
		t.Fatal(err)
	}
	// Now both should be reachable within ~20 picks.
	seen := map[int64]bool{}
	for i := 0; i < 20; i++ {
		acc, err := Pick(context.Background(), testMasterKey, tid, "openai")
		if err != nil {
			t.Fatal(err)
		}
		seen[acc.ConnectionID] = true
	}
	if !seen[id2] {
		t.Fatalf("InvalidateCache did not expose new connection: %+v", seen)
	}
}
