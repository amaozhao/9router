package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/redisx"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
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

func TestDefaultsHaveAllPlans(t *testing.T) {
	d := Defaults()
	for _, plan := range []string{"free", "starter", "pro", "enterprise"} {
		if _, ok := d[plan]; !ok {
			t.Fatalf("plan %q missing from defaults", plan)
		}
	}
	if d["enterprise"].Tokens != nil || d["enterprise"].Requests != nil {
		t.Fatalf("enterprise should be unlimited (nil limits)")
	}
}

func TestTodayKeyFormat(t *testing.T) {
	k := todayKeyCN()
	if len(k) != 10 || k[4] != '-' || k[7] != '-' {
		t.Fatalf("todayKeyCN: %q", k)
	}
}

func newTenantWithPlan(t *testing.T, plan string) int64 {
	t.Helper()
	ctx := context.Background()
	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan, status) VALUES ($1, $2, 'active') RETURNING id
	`, fmt.Sprintf("quota-%d", time.Now().UnixNano()), plan).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
		_ = redisx.C().Del(ctx, cfgKey(tid), reqKey(tid), tokKey(tid)).Err()
	})
	return tid
}

func TestEnforceUnderRequestLimit(t *testing.T) {
	requireDeps(t)
	tid := newTenantWithPlan(t, "free") // free → 1000 reqs
	tc := &tenantctx.TenantContext{TenantID: tid, Plan: "free"}
	for i := 0; i < 5; i++ {
		if err := Enforce(context.Background(), tc); err != nil {
			t.Fatalf("Enforce #%d: %v", i, err)
		}
	}
}

func TestEnforceTokenLimitExceeded(t *testing.T) {
	requireDeps(t)
	tid := newTenantWithPlan(t, "free")
	tc := &tenantctx.TenantContext{TenantID: tid, Plan: "free"}
	// Free plan = 100_000 tokens; preload that many.
	if err := IncrementTokens(context.Background(), tid, 100_000); err != nil {
		t.Fatal(err)
	}
	err := Enforce(context.Background(), tc)
	if err == nil {
		t.Fatal("expected rate limit error after exhausting tokens")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "rate_limit_exceeded" {
		t.Fatalf("wrong code: %v", err)
	}
}

func TestEnterpriseUnlimited(t *testing.T) {
	requireDeps(t)
	tid := newTenantWithPlan(t, "enterprise")
	tc := &tenantctx.TenantContext{TenantID: tid, Plan: "enterprise"}
	// Even after pushing tokens absurdly high, enforcement should still pass.
	if err := IncrementTokens(context.Background(), tid, 999_999_999); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := Enforce(context.Background(), tc); err != nil {
			t.Fatalf("Enforce #%d under enterprise: %v", i, err)
		}
	}
}

func TestIncrementTokensIgnoresZeroAndNegative(t *testing.T) {
	requireDeps(t)
	tid := newTenantWithPlan(t, "starter")
	if err := IncrementTokens(context.Background(), tid, 0); err != nil {
		t.Fatal(err)
	}
	if err := IncrementTokens(context.Background(), tid, -50); err != nil {
		t.Fatal(err)
	}
	usage, err := ReadUsage(context.Background(), tid, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Tokens.Used != 0 {
		t.Fatalf("zero/negative additions should not change counter: %d", usage.Tokens.Used)
	}
}

func TestReadUsageReportsLimitsAndUse(t *testing.T) {
	requireDeps(t)
	tid := newTenantWithPlan(t, "starter")
	if err := IncrementTokens(context.Background(), tid, 1234); err != nil {
		t.Fatal(err)
	}
	usage, err := ReadUsage(context.Background(), tid, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Tokens.Used != 1234 {
		t.Fatalf("tokens used: got %d", usage.Tokens.Used)
	}
	if usage.Tokens.Limit == nil || *usage.Tokens.Limit != 1_000_000 {
		t.Fatalf("starter token limit: %+v", usage.Tokens.Limit)
	}
	if usage.Requests.Limit == nil || *usage.Requests.Limit != 10_000 {
		t.Fatalf("starter request limit: %+v", usage.Requests.Limit)
	}
}

func TestInvalidateCacheForcesReload(t *testing.T) {
	requireDeps(t)
	tid := newTenantWithPlan(t, "free")
	tc := &tenantctx.TenantContext{TenantID: tid, Plan: "free"}
	if err := Enforce(context.Background(), tc); err != nil {
		t.Fatal(err)
	}
	if err := InvalidateCache(context.Background(), tid); err != nil {
		t.Fatal(err)
	}
	// re-loading should still work
	if err := Enforce(context.Background(), tc); err != nil {
		t.Fatal(err)
	}
}
