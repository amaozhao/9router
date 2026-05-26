package admin

import (
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/httpx"
	"github.com/jackc/pgx/v5"
)

type inviteDTO struct {
	ID         int64      `json:"id"`
	Code       string     `json:"code"`
	MaxUses    int        `json:"maxUses"`
	UsedCount  int        `json:"usedCount"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	Note       *string    `json:"note"`
	CreatedBy  *int64     `json:"createdBy"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type createInviteReq struct {
	MaxUses   *int    `json:"maxUses"`
	ExpiresAt *string `json:"expiresAt"`
	Note      *string `json:"note"`
}

func (d *Deps) MintInvite(w http.ResponseWriter, r *http.Request) {
	s, err := RequireSuperAdmin(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	var req createInviteReq
	_ = httpx.ReadJSON(r, &req)
	maxUses := 1
	if req.MaxUses != nil && *req.MaxUses >= 1 {
		maxUses = *req.MaxUses
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, *req.ExpiresAt); err == nil {
			expiresAt = &t
		}
	}

	// Retry up to 5 times on UNIQUE collisions
	var inv inviteDTO
	for i := 0; i < 5; i++ {
		code := newInviteCode()
		err := db.Pool().QueryRow(r.Context(), `
			INSERT INTO invite_codes (code, max_uses, expires_at, note, created_by)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, code, max_uses, used_count, expires_at, note, created_by, enabled, created_at
		`, code, maxUses, expiresAt, req.Note, s.UserID).Scan(
			&inv.ID, &inv.Code, &inv.MaxUses, &inv.UsedCount, &inv.ExpiresAt,
			&inv.Note, &inv.CreatedBy, &inv.Enabled, &inv.CreatedAt,
		)
		if err == nil {
			httpx.WriteJSON(w, http.StatusOK, inv)
			return
		}
		if strings.Contains(err.Error(), "invite_codes_code") {
			continue // try a new code
		}
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteError(w, errs.Internal("Could not mint a unique invite code"))
}

func (d *Deps) ListInvites(w http.ResponseWriter, r *http.Request) {
	_, err := RequireSuperAdmin(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rows, err := db.Pool().Query(r.Context(),
		`SELECT id, code, max_uses, used_count, expires_at, note, created_by, enabled, created_at
		 FROM invite_codes ORDER BY id DESC`)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := []inviteDTO{}
	for rows.Next() {
		var inv inviteDTO
		if err := rows.Scan(&inv.ID, &inv.Code, &inv.MaxUses, &inv.UsedCount, &inv.ExpiresAt,
			&inv.Note, &inv.CreatedBy, &inv.Enabled, &inv.CreatedAt); err != nil {
			httpx.WriteError(w, err)
			return
		}
		items = append(items, inv)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (d *Deps) DisableInvite(w http.ResponseWriter, r *http.Request) {
	_, err := RequireSuperAdmin(r, d.Cfg)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	code := r.PathValue("code")
	if code == "" {
		httpx.WriteError(w, errs.Validation("code path param required"))
		return
	}
	tag, err := db.Pool().Exec(r.Context(),
		`UPDATE invite_codes SET enabled = FALSE WHERE code = $1`, code)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, errs.NotFound("Invite code not found"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code})
}

// newInviteCode returns a 12-char base36 token from secure random bytes,
// with visually ambiguous letters (I/L/O) dropped. Matches the Node generator.
func newInviteCode() string {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // skip I, L, O, 0, 1
	out := make([]byte, 12)
	buf := make([]byte, 12)
	for {
		_, _ = rand.Read(buf)
		ok := true
		for i, b := range buf {
			out[i] = alphabet[int(b)%len(alphabet)]
		}
		if ok {
			return string(out)
		}
	}
}

// pgx unused-import sentinel
var _ = pgx.ErrNoRows
var _ = errors.New
