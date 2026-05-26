// worker — background loops for usage aggregation + OAuth token refresh.
// Runs forever; stop with SIGINT/SIGTERM.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/redisx"
	"github.com/amaozhao/lazirouter/internal/worker"
)

func main() {
	cfg := config.Load()
	logger.Init("worker", cfg.LogLevel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.Init(ctx, cfg.DatabaseURL); err != nil {
		logger.Error("db init failed", "err", err)
		os.Exit(1)
	}
	if err := redisx.Init(cfg.RedisURL); err != nil {
		logger.Error("redis init failed", "err", err)
		os.Exit(1)
	}

	// Register OAuth refreshers for known providers.
	worker.Register("claude", worker.RefreshClaudeOAuth)
	worker.Register("codex", worker.RefreshOAuth2)

	worker.ScheduleAggregator(ctx, time.Duration(cfg.WorkerInterval)*time.Second)
	worker.ScheduleRefresher(ctx, cfg.MasterKey, 30*time.Second)
	logger.Info("worker started",
		"aggregator_interval_s", cfg.WorkerInterval,
		"refresher_interval_s", 30,
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	logger.Info("shutting down", "signal", s.String())
	cancel()
	db.Close()
	redisx.Close()
}
