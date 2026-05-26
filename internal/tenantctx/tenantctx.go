// Package tenantctx resolves `sk-lr-…` API keys to a per-request tenant
// context. Caches in Redis (apikey:<sha256>) for 60s; negative cache 60s.
package tenantctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

// TenantContext is what every authenticated /v1/* handler gets.
type TenantContext struct {
	TenantID     int64          `json:"tenantId"`
	APIKeyID     int64          `json:"apiKeyId"`
	Scopes       map[string]any `json:"scopes,omitempty"`
	RateLimitRPM *int           `json:"rateLimitRpm,omitempty"`
	Plan         string         `json:"plan"`
	TenantStatus string         `json:"tenantStatus"`
}

const cacheTTL = 60 * time.Second
const negativeTTL = 60 * time.Second

// ResolveAPIKey accepts the bearer / x-api-key string and returns the tenant
// context, hitting Redis cache before the DB.
func ResolveAPIKey(ctx context.Context, key string) (*TenantContext, error) {
	if !strings.HasPrefix(key, "sk-lr-") {
		return nil, errs.Auth("Invalid API key format")
	}
	hash := cryptox.SHA256Hex(key)
	cacheKey := "apikey:" + hash
	rc := redisx.C()

	// 1) cache hit
	if cached, err := rc.Get(ctx, cacheKey).Result(); err == nil {
		if cached == "__not_found__" {
			return nil, errs.Auth("Unknown API key")
		}
		var t TenantContext
		if err := json.Unmarshal([]byte(cached), &t); err == nil {
			return &t, nil
		}
	}

	// 2) DB lookup
	var t TenantContext
	var scopesRaw []byte
	var rpm *int
	row := db.Pool().QueryRow(ctx, `
		SELECT k.id, k.tenant_id, k.scopes, k.rate_limit_rpm,
		       t.plan, t.status
		FROM api_keys k JOIN tenants t ON t.id = k.tenant_id
		WHERE k.key_hash = $1 AND k.revoked_at IS NULL
		LIMIT 1
	`, hash)
	if err := row.Scan(&t.APIKeyID, &t.TenantID, &scopesRaw, &rpm, &t.Plan, &t.TenantStatus); err != nil {
		if isNoRows(err) {
			_ = rc.Set(ctx, cacheKey, "__not_found__", negativeTTL).Err()
			return nil, errs.Auth("Unknown API key")
		}
		return nil, fmt.Errorf("api_keys lookup: %w", err)
	}
	t.RateLimitRPM = rpm
	if len(scopesRaw) > 0 {
		_ = json.Unmarshal(scopesRaw, &t.Scopes)
	}

	// 3) cache + fire-and-forget last_used_at update
	buf, _ := json.Marshal(t)
	_ = rc.Set(ctx, cacheKey, buf, cacheTTL).Err()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := db.Pool().Exec(bg, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, t.APIKeyID); err != nil {
			logger.Warn("last_used_at update failed", "err", err)
		}
	}()
	return &t, nil
}

// EnforceTenantActive returns Forbidden when the tenant is suspended/disabled.
func EnforceTenantActive(t *TenantContext) error {
	if t.TenantStatus != "active" {
		return errs.Forbidden("Tenant is "+t.TenantStatus, map[string]any{"tenant_status": t.TenantStatus})
	}
	return nil
}

// InvalidateByKey drops the cache for a single API key (call from admin path
// after revoke / rotate).
func InvalidateByKey(ctx context.Context, key string) error {
	return redisx.C().Del(ctx, "apikey:"+cryptox.SHA256Hex(key)).Err()
}

// InvalidateByHash for callers that only have the hex hash.
func InvalidateByHash(ctx context.Context, hash string) error {
	return redisx.C().Del(ctx, "apikey:"+hash).Err()
}

// helper so we don't import pgx in this file's surface
func isNoRows(err error) bool {
	return err != nil && (errors.Is(err, errNoRows) ||
		strings.Contains(err.Error(), "no rows"))
}

var errNoRows = errors.New("no rows in result set")
