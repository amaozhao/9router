package combo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
)

const defaultTestDBURL = "postgres://router:router_dev_pw@localhost:55432/router"

var depsReady bool

func TestMain(m *testing.M) {
	pgURL := os.Getenv("TEST_DATABASE_URL")
	if pgURL == "" {
		pgURL = defaultTestDBURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.Init(ctx, pgURL); err == nil {
		if err := db.Pool().Ping(ctx); err == nil {
			depsReady = true
		}
	}
	os.Exit(m.Run())
}

func TestResolveEmptyInput(t *testing.T) {
	_, err := Resolve(context.Background(), 1, "")
	if err == nil {
		t.Fatal("expected validation error for empty model")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "validation_error" {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestResolveExplicitProviderModel(t *testing.T) {
	got, err := Resolve(context.Background(), 1, "openai:gpt-4o-mini")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Provider != "openai" || got[0].UpstreamModel != "gpt-4o-mini" {
		t.Fatalf("explicit: %+v", got)
	}
}

func TestResolveHeuristics(t *testing.T) {
	cases := []struct {
		in       string
		provider string
	}{
		{"gpt-4o-mini", "codex"},
		{"o1-preview", "codex"},
		{"chatgpt-4o", "codex"},
		{"claude-3-5-sonnet", "claude"},
		{"gemini-2.0-flash", "gemini"},
		{"glm-4-flash", "glm"},
		{"deepseek-chat", "deepseek"},
		{"mock-mini", "mock"},
		{"meta-llama-3.1", "openai"}, // default
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := Resolve(context.Background(), 1, c.in)
			if err != nil {
				t.Fatal(err)
			}
			if got[0].Provider != c.provider {
				t.Fatalf("%q: got %q want %q", c.in, got[0].Provider, c.provider)
			}
			if got[0].UpstreamModel != c.in {
				t.Fatalf("upstream model should be pass-through: %q vs %q", got[0].UpstreamModel, c.in)
			}
		})
	}
}

func TestResolveComboSlug(t *testing.T) {
	if !depsReady {
		t.Skipf("skipping: dev Postgres not available")
	}
	ctx := context.Background()
	var tid int64
	if err := db.Pool().QueryRow(ctx, `
		INSERT INTO tenants (name, plan) VALUES ($1, 'free') RETURNING id
	`, fmt.Sprintf("combo-%d", time.Now().UnixNano())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid) })

	nodes := `[{"provider":"openai","model":"gpt-4o"},{"provider":"claude","model":"claude-3-5-sonnet"}]`
	_, err := db.Pool().Exec(ctx, `
		INSERT INTO combos (tenant_id, slug, nodes, enabled) VALUES ($1, 'fast-chain', $2::jsonb, TRUE)
	`, tid, nodes)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(ctx, tid, "combo:fast-chain")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(got))
	}
	if got[0].Provider != "openai" || got[1].Provider != "claude" {
		t.Fatalf("attempt order wrong: %+v", got)
	}
	if got[0].SourceCombo != "fast-chain" || got[0].Step != 0 || got[1].Step != 1 {
		t.Fatalf("attempt metadata wrong: %+v", got)
	}
}

func TestResolveUnknownCombo(t *testing.T) {
	if !depsReady {
		t.Skipf("skipping: dev Postgres not available")
	}
	_, err := Resolve(context.Background(), 999_999_999, "combo:does-not-exist")
	if err == nil {
		t.Fatal("expected validation error for unknown combo")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "validation_error" {
		t.Fatalf("wrong code: %v", err)
	}
}
