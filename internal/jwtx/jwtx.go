// Package jwtx wraps HS256 JWT signing/verification. Matches the Node payload
// shape: {sub, tid, role, email, iat, exp}.
package jwtx

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const TokenLifetime = 12 * time.Hour

// Session is the canonical JWT claim payload.
type Session struct {
	UserID   int64  `json:"sub"`
	TenantID int64  `json:"tid"`
	Role     string `json:"role"`
	Email    string `json:"email,omitempty"`
	jwt.RegisteredClaims
}

// Sign issues a new token signed with `secret`.
func Sign(secret []byte, s Session) (string, error) {
	now := time.Now()
	s.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(TokenLifetime)),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, s)
	return t.SignedString(secret)
}

// Verify parses + validates the token, returning the claims.
func Verify(secret []byte, raw string) (*Session, error) {
	out := &Session{}
	tok, err := jwt.ParseWithClaims(raw, out, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "HS256" {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("token invalid")
	}
	return out, nil
}
