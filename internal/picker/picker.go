// Package picker chooses the next active upstream account for a (tenant, provider)
// pair. Implements weighted round-robin + cooldown via Redis, matching the
// Node accountPicker.js semantics exactly so failover behaviour is identical.
package picker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/redisx"
	"github.com/redis/go-redis/v9"
)

const (
	connCacheTTL       = 5 * time.Second
	cooldownDefault    = 60 * time.Second
	cooldownBackoffMax = 30 * time.Minute
)

// Account is what handlers receive after a successful pick.
type Account struct {
	ConnectionID int64
	Name         string
	Weight       int
	Credentials  map[string]any
	Metadata     map[string]any
}

type cachedConn struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Weight       int             `json:"weight"`
	EncryptedB64 string          `json:"credentials_encrypted"`
	Metadata     json.RawMessage `json:"metadata"`
}

// Pick returns the next eligible connection or NoAccountAvailableError when
// all are in cooldown / none configured.
func Pick(ctx context.Context, masterKey []byte, tenantID int64, provider string) (*Account, error) {
	conns, err := loadActive(ctx, tenantID, provider)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, errs.NoAccountAvailable(map[string]any{
			"tenantId": tenantID, "provider": provider, "reason": "no connections configured",
		})
	}

	rc := redisx.C()

	// Pipeline: EXISTS for each cooldown key
	pipe := rc.Pipeline()
	checks := make([]*redis.IntCmd, len(conns))
	for i, c := range conns {
		checks[i] = pipe.Exists(ctx, cooldownKey(tenantID, provider, c.ID))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		logger.Warn("cooldown probe pipeline failed; assuming all alive", "err", err)
	}

	alive := make([]cachedConn, 0, len(conns))
	for i, c := range conns {
		if checks[i].Val() == 0 {
			alive = append(alive, c)
		}
	}
	if len(alive) == 0 {
		return nil, errs.NoAccountAvailable(map[string]any{
			"tenantId": tenantID, "provider": provider, "reason": "all in cooldown",
		})
	}

	// Weighted virtual ring + atomic cursor
	total := 0
	for _, c := range alive {
		total += max1(c.Weight)
	}
	cursor, err := rc.Incr(ctx, cursorKey(tenantID, provider)).Result()
	if err != nil {
		return nil, err
	}
	slot := int(((cursor-1)%int64(total) + int64(total)) % int64(total))
	acc := 0
	picked := alive[len(alive)-1]
	for _, c := range alive {
		acc += max1(c.Weight)
		if slot < acc {
			picked = c
			break
		}
	}

	encBytes, err := base64StdDecode(picked.EncryptedB64)
	if err != nil {
		return nil, fmt.Errorf("decode encrypted creds: %w", err)
	}
	creds, err := cryptox.DecryptForTenant(masterKey, tenantID, encBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt creds: %w", err)
	}
	var meta map[string]any
	if len(picked.Metadata) > 0 {
		_ = json.Unmarshal(picked.Metadata, &meta)
	}
	return &Account{
		ConnectionID: picked.ID,
		Name:         picked.Name,
		Weight:       picked.Weight,
		Credentials:  creds,
		Metadata:     meta,
	}, nil
}

// MarkCooldown sets / extends a cooldown for one connection. Repeated failures
// within an hour double the duration up to 30 min.
func MarkCooldown(ctx context.Context, tenantID int64, provider string, connID int64) (time.Duration, error) {
	rc := redisx.C()
	strikesKey := fmt.Sprintf("cooldown_strikes:%d:%s:%d", tenantID, provider, connID)
	strikes, err := rc.Incr(ctx, strikesKey).Result()
	if err != nil {
		return 0, err
	}
	_ = rc.Expire(ctx, strikesKey, time.Hour).Err()
	dur := time.Duration(math.Min(
		float64(cooldownDefault)*math.Pow(2, float64(strikes-1)),
		float64(cooldownBackoffMax),
	))
	if err := rc.Set(ctx, cooldownKey(tenantID, provider, connID), "1", dur).Err(); err != nil {
		return 0, err
	}
	logger.Info("cooldown set", "tenantId", tenantID, "provider", provider, "connectionId", connID, "strikes", strikes, "backoff", dur.String())
	return dur, nil
}

// ClearCooldown removes a cooldown manually (e.g. on connection re-enable).
func ClearCooldown(ctx context.Context, tenantID int64, provider string, connID int64) error {
	rc := redisx.C()
	_ = rc.Del(ctx, cooldownKey(tenantID, provider, connID)).Err()
	_ = rc.Del(ctx, fmt.Sprintf("cooldown_strikes:%d:%s:%d", tenantID, provider, connID)).Err()
	return nil
}

// InvalidateCache drops the cached connection list (call on insert/update/delete).
func InvalidateCache(ctx context.Context, tenantID int64, provider string) error {
	return redisx.C().Del(ctx, connCacheKey(tenantID, provider)).Err()
}

func loadActive(ctx context.Context, tenantID int64, provider string) ([]cachedConn, error) {
	rc := redisx.C()
	key := connCacheKey(tenantID, provider)
	if cached, err := rc.Get(ctx, key).Result(); err == nil && cached != "" {
		var conns []cachedConn
		if err := json.Unmarshal([]byte(cached), &conns); err == nil {
			return conns, nil
		}
	}
	rows, err := db.Pool().Query(ctx, `
		SELECT id, name, weight, credentials_encrypted, metadata
		FROM connections
		WHERE tenant_id = $1 AND provider = $2 AND enabled = TRUE
		ORDER BY id ASC
	`, tenantID, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cachedConn{}
	for rows.Next() {
		var c cachedConn
		var enc []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.Weight, &enc, &c.Metadata); err != nil {
			return nil, err
		}
		c.EncryptedB64 = base64StdEncode(enc)
		out = append(out, c)
	}
	buf, _ := json.Marshal(out)
	_ = rc.Set(ctx, key, buf, connCacheTTL).Err()
	return out, nil
}

func cursorKey(t int64, p string) string   { return fmt.Sprintf("account_cursor:%d:%s", t, p) }
func cooldownKey(t int64, p string, c int64) string {
	return fmt.Sprintf("cooldown:%d:%s:%d", t, p, c)
}
func connCacheKey(t int64, p string) string { return fmt.Sprintf("conn_cache:%d:%s", t, p) }

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// tiny shims so we don't add encoding/base64 to every call site
func base64StdEncode(b []byte) string         { return stdEncode(b) }
func base64StdDecode(s string) ([]byte, error) { return stdDecode(s) }
