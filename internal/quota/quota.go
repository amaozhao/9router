// Package quota implements per-tenant rate limiting and daily request/token
// caps. Counters live in Redis with 48 h TTL; the day key uses Asia/Shanghai
// so the rollover matches operator expectations.
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/redisx"
	"github.com/amaozhao/lazirouter/internal/tenantctx"
)

// PlanQuota is the default daily cap per plan. nil means unlimited.
type PlanQuota struct {
	Tokens   *int64 `json:"tokens"`
	Requests *int   `json:"requests"`
}

var planDefaults = map[string]PlanQuota{
	"free":       {ptrI64(100_000), ptrInt(1_000)},
	"starter":    {ptrI64(1_000_000), ptrInt(10_000)},
	"pro":        {ptrI64(10_000_000), ptrInt(100_000)},
	"enterprise": {nil, nil},
}

// Defaults exposes the plan map for read-only callers (admin UI).
func Defaults() map[string]PlanQuota { return planDefaults }

func todayKeyCN() string {
	t := time.Now().UTC().Add(8 * time.Hour) // CST = UTC+8
	return t.Format("2006-01-02")
}

func reqKey(tenantID int64) string {
	return fmt.Sprintf("quota:%d:%s:req", tenantID, todayKeyCN())
}
func tokKey(tenantID int64) string {
	return fmt.Sprintf("quota:%d:%s:tok", tenantID, todayKeyCN())
}
func cfgKey(tenantID int64) string {
	return fmt.Sprintf("quota:cfg:%d", tenantID)
}

// effective returns the actual cap (override or plan default) for a tenant.
type effectiveCfg struct {
	TokenLimit   *int64 `json:"daily_token_limit"`
	RequestLimit *int   `json:"daily_request_limit"`
}

func loadEffective(ctx context.Context, tenantID int64, plan string) (*effectiveCfg, error) {
	rc := redisx.C()
	if cached, err := rc.Get(ctx, cfgKey(tenantID)).Result(); err == nil {
		var c effectiveCfg
		if err := json.Unmarshal([]byte(cached), &c); err == nil {
			return &c, nil
		}
	}
	var c effectiveCfg
	err := db.Pool().QueryRow(ctx, `
		SELECT daily_token_limit, daily_request_limit
		FROM tenant_quotas WHERE tenant_id = $1
	`, tenantID).Scan(&c.TokenLimit, &c.RequestLimit)
	if err != nil {
		// no override → fall back to plan defaults
		pd := planDefaults[plan]
		c.TokenLimit = pd.Tokens
		c.RequestLimit = pd.Requests
	} else {
		// fill nulls with plan defaults
		pd := planDefaults[plan]
		if c.TokenLimit == nil {
			c.TokenLimit = pd.Tokens
		}
		if c.RequestLimit == nil {
			c.RequestLimit = pd.Requests
		}
	}
	buf, _ := json.Marshal(c)
	_ = rc.Set(ctx, cfgKey(tenantID), buf, 5*time.Minute).Err()
	return &c, nil
}

// Enforce pre-checks the request count and the (already-incremented) token
// count for today. Increments the request counter atomically. Returns
// RateLimitError when over budget.
func Enforce(ctx context.Context, t *tenantctx.TenantContext) error {
	cfg, err := loadEffective(ctx, t.TenantID, t.Plan)
	if err != nil {
		return err
	}
	rc := redisx.C()
	// 1) pre-check tokens (no increment — happens after upstream returns)
	if cfg.TokenLimit != nil {
		raw, _ := rc.Get(ctx, tokKey(t.TenantID)).Result()
		used, _ := strconv.ParseInt(raw, 10, 64)
		if used >= *cfg.TokenLimit {
			return errs.RateLimit("Daily token quota exceeded",
				map[string]any{"used": used, "limit": *cfg.TokenLimit})
		}
	}
	// 2) atomic INCR for request count
	if cfg.RequestLimit != nil {
		n, err := rc.Incr(ctx, reqKey(t.TenantID)).Result()
		if err != nil {
			return err
		}
		if n == 1 {
			_ = rc.Expire(ctx, reqKey(t.TenantID), 48*time.Hour).Err()
		}
		if int(n) > *cfg.RequestLimit {
			return errs.RateLimit("Daily request quota exceeded",
				map[string]any{"used": n, "limit": *cfg.RequestLimit})
		}
	}
	return nil
}

// IncrementTokens is called by usage recording after the upstream reply.
func IncrementTokens(ctx context.Context, tenantID int64, total int64) error {
	if total <= 0 {
		return nil
	}
	rc := redisx.C()
	n, err := rc.IncrBy(ctx, tokKey(tenantID), total).Result()
	if err != nil {
		return err
	}
	if n == total {
		_ = rc.Expire(ctx, tokKey(tenantID), 48*time.Hour).Err()
	}
	return nil
}

// Usage reports today's used/limit for both meters. Used by GET /api/me/quota.
type Usage struct {
	Date     string `json:"date"`
	Requests struct {
		Used  int64  `json:"used"`
		Limit *int   `json:"limit"`
	} `json:"requests"`
	Tokens struct {
		Used  int64  `json:"used"`
		Limit *int64 `json:"limit"`
	} `json:"tokens"`
}

func ReadUsage(ctx context.Context, tenantID int64, plan string) (*Usage, error) {
	cfg, err := loadEffective(ctx, tenantID, plan)
	if err != nil {
		return nil, err
	}
	rc := redisx.C()
	rRaw, _ := rc.Get(ctx, reqKey(tenantID)).Result()
	tRaw, _ := rc.Get(ctx, tokKey(tenantID)).Result()
	rUsed, _ := strconv.ParseInt(rRaw, 10, 64)
	tUsed, _ := strconv.ParseInt(tRaw, 10, 64)
	u := &Usage{Date: todayKeyCN()}
	u.Requests.Used, u.Requests.Limit = rUsed, cfg.RequestLimit
	u.Tokens.Used, u.Tokens.Limit = tUsed, cfg.TokenLimit
	return u, nil
}

// InvalidateCache forces a re-read of the per-tenant override on next Enforce.
func InvalidateCache(ctx context.Context, tenantID int64) error {
	return redisx.C().Del(ctx, cfgKey(tenantID)).Err()
}

func ptrI64(v int64) *int64 { return &v }
func ptrInt(v int) *int     { return &v }
