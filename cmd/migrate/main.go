// migrate — idempotent migration runner. Reads migrations/*.sql in lexical
// order and applies any not yet recorded in schema_migrations. Each migration
// runs in a single transaction together with its schema_migrations insert.
//
// Resolves the migrations directory in this order:
//   1. $MIGRATIONS_DIR (if set)
//   2. /app/migrations  (bundled in the Docker image)
//   3. ./migrations     (cwd, for local dev)
//   4. ../migrations    (when invoked from scripts/)
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/jackc/pgx/v5"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://router:router_dev_pw@localhost:55432/router"
	}
	dir := resolveDir()
	if dir == "" {
		fmt.Fprintln(os.Stderr, "migrations directory not found (set MIGRATIONS_DIR)")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		die("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		die("create schema_migrations: %v", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		die("select schema_migrations: %v", err)
	}
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		applied[v] = true
	}
	rows.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		die("readdir %s: %v", dir, err)
	}
	var sqls []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			sqls = append(sqls, e.Name())
		}
	}
	sort.Strings(sqls)

	count := 0
	for _, f := range sqls {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			fmt.Printf("[skip] %s\n", version)
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			die("read %s: %v", f, err)
		}
		fmt.Printf("[apply] %s\n", version)
		tx, err := conn.Begin(ctx)
		if err != nil {
			die("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			die("[fail] %s: %v", version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING`,
			version); err != nil {
			_ = tx.Rollback(ctx)
			die("[fail] %s record: %v", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			die("[fail] %s commit: %v", version, err)
		}
		count++
	}
	fmt.Printf("Done. Applied %d new migration(s).\n", count)

	// Touch the config package so the unused import lint doesn't fire when we
	// later want to honour it from here. (No-op at runtime.)
	_ = config.Loaded
}

func resolveDir() string {
	candidates := []string{
		os.Getenv("MIGRATIONS_DIR"),
		"/app/migrations",
		"./migrations",
		"../migrations",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
