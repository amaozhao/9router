package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/cryptox"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/amaozhao/lazirouter/internal/jwtx"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type signupReq struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	TenantName string `json:"tenantName"`
	InviteCode string `json:"inviteCode"`
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResp struct {
	Token         string         `json:"token"`
	User          map[string]any `json:"user"`
	Tenant        map[string]any `json:"tenant"`
	DefaultAPIKey map[string]any `json:"defaultApiKey,omitempty"`
}

// Signup creates a tenant + owner user. Requires inviteCode unless the email
// is configured as super-admin (bootstrap path).
func (d *Deps) Signup(w http.ResponseWriter, r *http.Request) {
	var req signupReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.TenantName = strings.TrimSpace(req.TenantName)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		httpx.WriteError(w, errs.Validation("Valid email required"))
		return
	}
	if len(req.Password) < 8 {
		httpx.WriteError(w, errs.Validation("Password must be ≥ 8 chars"))
		return
	}
	if req.TenantName == "" {
		httpx.WriteError(w, errs.Validation("tenantName required"))
		return
	}
	isSuper := d.Cfg.IsSuperAdminEmail(req.Email)
	if req.InviteCode == "" && !isSuper {
		httpx.WriteError(w, errs.Validation("Invite code required",
			map[string]any{"field": "inviteCode"}))
		return
	}

	ctx := r.Context()
	tx, err := db.Pool().Begin(ctx)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer tx.Rollback(ctx)

	// 1. Atomic invite consumption (super-admin bypass)
	if !isSuper {
		var id int64
		var maxUses, usedCount int
		err := tx.QueryRow(ctx, `
			SELECT id, max_uses, used_count FROM invite_codes
			WHERE code = $1 AND enabled = TRUE
			  AND (expires_at IS NULL OR expires_at > now())
			FOR UPDATE
		`, req.InviteCode).Scan(&id, &maxUses, &usedCount)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, errs.Validation("Invalid or expired invite code",
				map[string]any{"field": "inviteCode"}))
			return
		}
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		if usedCount >= maxUses {
			httpx.WriteError(w, errs.Validation("Invite code already used",
				map[string]any{"field": "inviteCode"}))
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE invite_codes SET used_count = used_count + 1 WHERE id = $1`, id); err != nil {
			httpx.WriteError(w, err)
			return
		}
	}

	// 2. Reject duplicate email
	var exists int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM users WHERE email = $1 LIMIT 1`, req.Email).Scan(&exists); err == nil {
		httpx.WriteError(w, errs.Validation("Email already registered", map[string]any{"field": "email"}))
		return
	}

	// 3. Create tenant + user
	var tenantID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO tenants (name, plan, status) VALUES ($1, 'free', 'active') RETURNING id`,
		req.TenantName).Scan(&tenantID); err != nil {
		httpx.WriteError(w, err)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var userID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, password_hash, role) VALUES ($1, $2, $3, 'owner') RETURNING id
	`, tenantID, req.Email, string(hash)).Scan(&userID); err != nil {
		httpx.WriteError(w, err)
		return
	}

	// 4. Mint default API key
	plain, keyHash, keyPrefix, err := cryptox.GenerateAPIKey()
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var apiKeyID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO api_keys (tenant_id, created_by, key_prefix, key_hash, name, scopes)
		VALUES ($1, $2, $3, $4, 'default', '{}'::jsonb) RETURNING id
	`, tenantID, userID, keyPrefix, keyHash).Scan(&apiKeyID); err != nil {
		httpx.WriteError(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, err)
		return
	}

	tok, err := jwtx.Sign([]byte(d.Cfg.JWTSecret), jwtx.Session{
		UserID: userID, TenantID: tenantID, Role: "owner", Email: req.Email,
	})
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, &authResp{
		Token:  tok,
		User:   map[string]any{"id": userID, "email": req.Email, "role": "owner", "isSuperAdmin": isSuper},
		Tenant: map[string]any{"id": tenantID, "name": req.TenantName, "plan": "free"},
		DefaultAPIKey: map[string]any{
			"keyPrefix": keyPrefix,
			"key":       plain,
		},
	})
}

// Login returns a JWT for an existing user.
func (d *Deps) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	var (
		userID, tenantID int64
		hash, role, name, plan string
	)
	err := db.Pool().QueryRow(r.Context(), `
		SELECT u.id, u.tenant_id, u.password_hash, u.role, t.name, t.plan
		FROM users u JOIN tenants t ON t.id = u.tenant_id
		WHERE u.email = $1 LIMIT 1
	`, req.Email).Scan(&userID, &tenantID, &hash, &role, &name, &plan)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, errs.Auth("Invalid credentials"))
		return
	}
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		httpx.WriteError(w, errs.Auth("Invalid credentials"))
		return
	}
	isSuper := d.Cfg.IsSuperAdminEmail(req.Email)
	tok, err := jwtx.Sign([]byte(d.Cfg.JWTSecret), jwtx.Session{
		UserID: userID, TenantID: tenantID, Role: role, Email: req.Email,
	})
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, &authResp{
		Token:  tok,
		User:   map[string]any{"id": userID, "email": req.Email, "role": role, "isSuperAdmin": isSuper},
		Tenant: map[string]any{"id": tenantID, "name": name, "plan": plan},
	})
}

// Me returns the current user/tenant for the bearer JWT.
func (d *Deps) Me(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSession(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var name, plan string
	err = db.Pool().QueryRow(r.Context(), `
		SELECT t.name, t.plan FROM users u JOIN tenants t ON t.id = u.tenant_id
		WHERE u.id = $1 AND u.tenant_id = $2 LIMIT 1
	`, s.UserID, s.TenantID).Scan(&name, &plan)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"user":   map[string]any{"id": s.UserID, "email": s.Email, "role": s.Role, "isSuperAdmin": s.IsSuperAdmin},
		"tenant": map[string]any{"id": s.TenantID, "name": name, "plan": plan},
	})
}

// Deps wires shared dependencies for admin handlers.
type Deps struct {
	Cfg *config.Config
}

// Ensure context import is used (Go vet)
var _ = context.Background
