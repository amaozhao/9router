package tenantctx

import (
	"context"
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

var depsReady bool

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

func TestResolveAPIKey_InvalidPrefix(t *testing.T) {
	_, err := ResolveAPIKey(context.Background(), "not-an-sklr-key")
	if err == nil {
		t.Fatal("expected auth error for missing prefix")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestResolveAPIKey_Unknown(t *testing.T) {
	requireDeps(t)
	// Unique fake key — guaranteed not in DB.
	key := fmt.Sprintf("sk-lr-unknown-%d", time.Now().UnixNano())
	_ = InvalidateByKey(context.Background(), key)
	_, err := ResolveAPIKey(context.Background(), key)
	if err == nil {
		t.Fatal("expected auth error for unknown key")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "auth_error" {
		t.Fatalf("wrong code: %v", err)
	}
	// Second call should hit negative cache (same outcome, faster).
	if _, err := ResolveAPIKey(context.Background(), key); err == nil {
		t.Fatal("negative cache did not preserve error")
	}
}

func newTenantAndKey(t *testing.T, plan, status string) (int64, string) {
	t.Helper()
	ctx := context.Background()
	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan, status) VALUES ($1, $2, $3) RETURNING id
	`, fmt.Sprintf("tctx-%d", time.Now().UnixNano()), plan, status).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	plain, hash, prefix, err := cryptox.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO api_keys (tenant_id, key_prefix, key_hash, name)
		VALUES ($1, $2, $3, 'test')
	`, tid, prefix, hash); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
		_ = InvalidateByKey(ctx, plain)
	})
	return tid, plain
}

func TestResolveAPIKey_Hit(t *testing.T) {
	requireDeps(t)
	tid, key := newTenantAndKey(t, "pro", "active")
	tc, err := ResolveAPIKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if tc.TenantID != tid {
		t.Fatalf("tenant id: got %d want %d", tc.TenantID, tid)
	}
	if tc.Plan != "pro" || tc.TenantStatus != "active" {
		t.Fatalf("payload: %+v", tc)
	}
	// Confirm cache: drop the row and the next call should still succeed
	// (proves it came from Redis).
	_, _ = db.Pool().Exec(context.Background(), `DELETE FROM api_keys WHERE tenant_id = $1`, tid)
	tc2, err := ResolveAPIKey(context.Background(), key)
	if err != nil {
		t.Fatalf("cache miss after row delete: %v", err)
	}
	if tc2.TenantID != tid {
		t.Fatalf("cache should have served the same context")
	}
}

func TestEnforceTenantActive(t *testing.T) {
	cases := []struct {
		status string
		ok     bool
	}{
		{"active", true},
		{"suspended", false},
		{"deleted", false},
	}
	for _, c := range cases {
		err := EnforceTenantActive(&TenantContext{TenantStatus: c.status})
		if c.ok && err != nil {
			t.Fatalf("status=%s should pass: %v", c.status, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("status=%s should fail", c.status)
		}
	}
}

func TestInvalidateByHash(t *testing.T) {
	requireDeps(t)
	// Just verifies the helper doesn't error on a missing key.
	if err := InvalidateByHash(context.Background(), "deadbeef"); err != nil {
		t.Fatal(err)
	}
}

// Sanity: confirm Redis is reachable so negative tests don't bring the
// whole test binary down with a panic on a nil client.
func TestRedisReachable(t *testing.T) {
	requireDeps(t)
	if _, err := redisx.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
