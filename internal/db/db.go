// Package db owns the global Postgres connection pool (pgxpool).
package db

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool *pgxpool.Pool
	once sync.Once
)

// Init connects to Postgres using the supplied URL. Idempotent.
func Init(ctx context.Context, url string) error {
	var err error
	once.Do(func() {
		var cfg *pgxpool.Config
		cfg, err = pgxpool.ParseConfig(url)
		if err != nil {
			err = fmt.Errorf("parse DATABASE_URL: %w", err)
			return
		}
		cfg.MaxConns = 30
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
	})
	return err
}

// Pool returns the global pool; panics if Init was not called.
func Pool() *pgxpool.Pool {
	if pool == nil {
		panic("db.Init has not been called")
	}
	return pool
}

// Close gracefully drains the pool.
func Close() {
	if pool != nil {
		pool.Close()
		pool = nil
	}
}

// Tx runs fn inside a single transaction; rolls back on error, commits on nil.
func Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}
