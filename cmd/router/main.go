// router — client-facing entry point (default :30100).
// Routes (incremental port from Node router/src/server.js):
//   GET  /health
//   POST /v1/chat/completions
//   POST /v1/messages
//   POST /v1/responses
//   POST /v1/embeddings
//   GET  /v1/models
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

func main() {
	cfg := config.Load()
	logger.Init("router", cfg.LogLevel)

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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("POST /v1/chat/completions", notImplemented)
	mux.HandleFunc("POST /v1/messages", notImplemented)
	mux.HandleFunc("POST /v1/responses", notImplemented)
	mux.HandleFunc("POST /v1/embeddings", notImplemented)
	mux.HandleFunc("GET /v1/models", notImplemented)

	addr := fmt.Sprintf(":%d", cfg.RouterPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           withAccessLog(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("router listening", "port", cfg.RouterPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	logger.Info("shutting down", "signal", s.String())

	shutdownCtx, sc := context.WithTimeout(context.Background(), 10*time.Second)
	defer sc()
	_ = srv.Shutdown(shutdownCtx)
	db.Close()
	redisx.Close()
	logger.Info("router stopped")
}

func health(w http.ResponseWriter, r *http.Request) {
	pong, err := redisx.Ping(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "redis": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"redis": pong,
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"error": map[string]any{
			"code":    "not_implemented",
			"message": "Go port in progress; route not wired yet",
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// withAccessLog emits one JSON line per request, matching the Node "http" log.
func withAccessLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: 200}
		h.ServeHTTP(ww, r)
		logger.Info("http",
			"method", r.Method,
			"url", r.URL.Path,
			"status", ww.status,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
