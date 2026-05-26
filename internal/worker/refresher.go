package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

// RefreshResult is what a refresher implementation returns.
type RefreshResult struct {
	Credentials map[string]any
	ExpiresAt   *time.Time
	Meta        map[string]any
}

// RefresherFunc rotates one connection's credentials.
type RefresherFunc func(ctx context.Context, creds map[string]any, hints RefreshHints) (*RefreshResult, error)

// RefreshHints lets a refresher know which row it is acting on.
type RefreshHints struct {
	TenantID     int64
	ConnectionID int64
	Name         string
}

var (
	refresherMu sync.RWMutex
	refreshers  = map[string]RefresherFunc{}
)

// Register installs a refresher for a provider key.
func Register(provider string, fn RefresherFunc) {
	refresherMu.Lock()
	refreshers[provider] = fn
	refresherMu.Unlock()
}

func lookup(provider string) RefresherFunc {
	refresherMu.RLock()
	defer refresherMu.RUnlock()
	return refreshers[provider]
}

// RefreshOnce scans connections expiring in the next 90 s and refreshes each.
// Returns the number of rows actually refreshed.
func RefreshOnce(ctx context.Context, masterKey []byte) (int, error) {
	started := time.Now()
	rows, err := db.Pool().Query(ctx, `
		SELECT id, tenant_id, provider, name, credentials_encrypted, metadata
		FROM connections
		WHERE auth_type = 'oauth' AND enabled = TRUE
		  AND oauth_expires_at IS NOT NULL
		  AND oauth_expires_at < now() + interval '90 seconds'
		ORDER BY oauth_expires_at ASC
		LIMIT 200
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type rowT struct {
		ID, TenantID int64
		Provider     string
		Name         string
		Encrypted    []byte
		Metadata     []byte
	}
	var scanned []rowT
	for rows.Next() {
		var r rowT
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Provider, &r.Name, &r.Encrypted, &r.Metadata); err != nil {
			return 0, err
		}
		scanned = append(scanned, r)
	}
	if len(scanned) == 0 {
		return 0, nil
	}

	refreshed := 0
	for _, r := range scanned {
		fn := lookup(r.Provider)
		if fn == nil {
			logger.Warn("no refresher registered, skipping", "provider", r.Provider)
			continue
		}
		// SETNX cross-process lock
		lockKey := fmt.Sprintf("lock:refresh:%d", r.ID)
		got, err := redisx.C().SetNX(ctx, lockKey, "1", 30*time.Second).Result()
		if err != nil || !got {
			continue
		}
		err = func() error {
			defer redisx.C().Del(ctx, lockKey)
			creds, err := cryptox.DecryptForTenant(masterKey, r.TenantID, r.Encrypted)
			if err != nil {
				return fmt.Errorf("decrypt: %w", err)
			}
			next, err := fn(ctx, creds, RefreshHints{TenantID: r.TenantID, ConnectionID: r.ID, Name: r.Name})
			if err != nil {
				return err
			}
			if next == nil || next.Credentials == nil {
				return fmt.Errorf("refresher returned no credentials")
			}
			blob, err := cryptox.EncryptForTenant(masterKey, r.TenantID, next.Credentials)
			if err != nil {
				return err
			}
			metaJSON := []byte("{}")
			if next.Meta != nil {
				metaJSON, _ = json.Marshal(next.Meta)
			}
			_, err = db.Pool().Exec(ctx, `
				UPDATE connections SET
				  credentials_encrypted = $1,
				  oauth_expires_at      = $2,
				  metadata              = COALESCE(metadata, '{}'::jsonb) || $3::jsonb,
				  updated_at            = now()
				WHERE id = $4
			`, blob, next.ExpiresAt, metaJSON, r.ID)
			if err != nil {
				return err
			}
			// invalidate picker cache
			_ = redisx.C().Del(ctx, fmt.Sprintf("conn_cache:%d:%s", r.TenantID, r.Provider)).Err()
			refreshed++
			logger.Info("refreshed", "tenantId", r.TenantID, "connectionId", r.ID, "provider", r.Provider)
			return nil
		}()
		if err != nil {
			logger.Error("refresh failed", "err", err.Error(), "connectionId", r.ID)
			errMsg := err.Error()
			if len(errMsg) > 200 {
				errMsg = errMsg[:200]
			}
			meta, _ := json.Marshal(map[string]any{
				"last_refresh_error":   errMsg,
				"last_refresh_attempt": time.Now().UTC().Format(time.RFC3339),
			})
			_, _ = db.Pool().Exec(ctx, `
				UPDATE connections SET metadata = COALESCE(metadata, '{}'::jsonb) || $1::jsonb
				WHERE id = $2
			`, meta, r.ID)
		}
	}
	logger.Info("refresh tick done", "refreshed", refreshed, "scanned", len(scanned), "ms", time.Since(started).Milliseconds())
	return refreshed, nil
}

// ScheduleRefresher fires RefreshOnce immediately and then every `interval`.
func ScheduleRefresher(ctx context.Context, masterKey []byte, interval time.Duration) {
	go func() {
		var busy bool
		run := func() {
			if busy {
				return
			}
			busy = true
			defer func() { busy = false }()
			if _, err := RefreshOnce(ctx, masterKey); err != nil {
				logger.Error("refresh tick error", "err", err)
			}
		}
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				run()
			case <-ctx.Done():
				return
			}
		}
	}()
}
