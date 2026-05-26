// Package admin holds the admin REST handlers and shared session helpers.
package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/jwtx"
)

// Session is the parsed claim block for the current request.
type Session struct {
	UserID       int64
	TenantID     int64
	Role         string
	Email        string
	IsSuperAdmin bool
}

// RequireSession parses the Authorization: Bearer JWT and returns the session.
// Returns AuthError on missing / invalid token.
func RequireSession(r *http.Request, cfg *config.Config) (*Session, error) {
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "Bearer ") {
		return nil, errs.Auth("Missing or malformed Authorization header")
	}
	claims, err := jwtx.Verify([]byte(cfg.JWTSecret), a[7:])
	if err != nil {
		return nil, errs.Auth("Invalid token: " + err.Error())
	}
	return &Session{
		UserID:       claims.UserID,
		TenantID:     claims.TenantID,
		Role:         claims.Role,
		Email:        claims.Email,
		IsSuperAdmin: cfg.IsSuperAdminEmail(claims.Email),
	}, nil
}

// RequireSuperAdmin upgrades RequireSession with a SUPER_ADMIN_EMAILS check;
// also fills email from DB if the (legacy) token did not include it.
func RequireSuperAdmin(r *http.Request, cfg *config.Config) (*Session, error) {
	s, err := RequireSession(r, cfg)
	if err != nil {
		return nil, err
	}
	if s.Email == "" {
		// rare: fetch from DB
		_ = db.Pool().QueryRow(context.Background(),
			`SELECT email FROM users WHERE id = $1`, s.UserID).Scan(&s.Email)
		s.IsSuperAdmin = cfg.IsSuperAdminEmail(s.Email)
	}
	if !s.IsSuperAdmin {
		return nil, errs.Forbidden("Super-admin only", map[string]any{"email": s.Email})
	}
	return s, nil
}
