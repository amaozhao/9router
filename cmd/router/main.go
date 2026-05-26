// router — client-facing entry point (default :30100).
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
	"github.com/amaozhao/lazirouter/internal/provider"
	"github.com/amaozhao/lazirouter/internal/redisx"
	"github.com/amaozhao/lazirouter/internal/router"
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

	deps := &router.Deps{
		Cfg:       cfg,
		OpenAI:    provider.NewOpenAI(),
		ClaudeSub: provider.NewClaudeSub(),
		CodexSub:  provider.NewCodexSub(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	// mount /v1/* handlers
	v1 := router.New(deps)
	mux.Handle("/v1/", v1)

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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func withAccessLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("handler panic",
					"method", r.Method, "url", r.URL.Path, "panic", rec)
				if !ww.headerWritten {
					http.Error(ww, `{"error":{"code":"internal_error","message":"handler panic"}}`, http.StatusInternalServerError)
					ww.status = http.StatusInternalServerError
				}
			}
			logger.Info("http",
				"method", r.Method,
				"url", r.URL.Path,
				"status", ww.status,
				"ms", time.Since(start).Milliseconds(),
			)
		}()
		h.ServeHTTP(ww, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status        int
	headerWritten bool
}

func (s *statusRecorder) WriteHeader(c int) {
	s.status = c
	s.headerWritten = true
	s.ResponseWriter.WriteHeader(c)
}
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
