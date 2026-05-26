// admin — back-office REST API (default :30200).
// Routes:
//   POST /auth/signup
//   POST /auth/login
//   GET  /auth/me
//   GET/POST/DELETE /api/keys[/:id]
//   GET/POST/PATCH/DELETE /api/connections[/:id]
//   GET/POST/PATCH/DELETE /api/combos[/:slug]
//   POST /api/oauth/{provider}/import
//   GET  /api/me/quota
//   GET  /api/admin/tenants                       (super-admin)
//   PUT  /api/admin/tenants/:id/quota             (super-admin)
//   POST/GET/DELETE /api/admin/invites[/:code]    (super-admin)
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

	"github.com/amaozhao/lazirouter/internal/admin"
	"github.com/amaozhao/lazirouter/internal/config"
	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/logger"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

func main() {
	cfg := config.Load()
	logger.Init("admin", cfg.LogLevel)

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

	d := &admin.Deps{Cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// Auth
	mux.HandleFunc("POST /auth/signup", d.Signup)
	mux.HandleFunc("POST /auth/login", d.Login)
	mux.HandleFunc("GET /auth/me", d.Me)

	// Keys
	mux.HandleFunc("GET /api/keys", d.ListKeys)
	mux.HandleFunc("POST /api/keys", d.CreateKey)
	mux.HandleFunc("DELETE /api/keys/{id}", d.RevokeKey)

	// Connections
	mux.HandleFunc("GET /api/connections", d.ListConnections)
	mux.HandleFunc("POST /api/connections", d.CreateConnection)
	mux.HandleFunc("PATCH /api/connections/{id}", d.UpdateConnection)
	mux.HandleFunc("DELETE /api/connections/{id}", d.DeleteConnection)

	// Combos
	mux.HandleFunc("GET /api/combos", d.ListCombos)
	mux.HandleFunc("POST /api/combos", d.CreateCombo)
	mux.HandleFunc("PATCH /api/combos/{slug}", d.UpdateCombo)
	mux.HandleFunc("DELETE /api/combos/{slug}", d.DeleteCombo)

	// OAuth (bring-your-own-token)
	mux.HandleFunc("POST /api/oauth/{provider}/import", d.ImportOAuth)

	// Quota / tenants (super-admin)
	mux.HandleFunc("GET /api/me/quota", d.MyQuota)
	mux.HandleFunc("GET /api/admin/tenants", d.ListTenants)
	mux.HandleFunc("PUT /api/admin/tenants/{id}/quota", d.PutTenantQuota)

	// Auto-routing
	mux.HandleFunc("GET /api/routing", d.ListRouting)
	mux.HandleFunc("PUT /api/routing/{scenario}", d.PutRouting)
	mux.HandleFunc("DELETE /api/routing/{scenario}", d.DeleteRouting)

	// Usage
	mux.HandleFunc("GET /api/usage/summary", d.UsageSummary)
	mux.HandleFunc("GET /api/usage/recent", d.UsageRecent)

	// Invites (super-admin)
	mux.HandleFunc("POST /api/admin/invites", d.MintInvite)
	mux.HandleFunc("GET /api/admin/invites", d.ListInvites)
	mux.HandleFunc("DELETE /api/admin/invites/{code}", d.DisableInvite)

	addr := fmt.Sprintf(":%d", cfg.AdminPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           withAccessLog(corsMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("admin listening", "port", cfg.AdminPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	logger.Info("shutting down", "signal", s.String())
	sc, sf := context.WithTimeout(context.Background(), 10*time.Second)
	defer sf()
	_ = srv.Shutdown(sc)
	db.Close()
	redisx.Close()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func corsMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("access-control-allow-origin", "*")
		w.Header().Set("access-control-allow-methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("access-control-allow-headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func withAccessLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: 200}
		h.ServeHTTP(ww, r)
		logger.Info("http",
			"method", r.Method, "url", r.URL.Path, "status", ww.status, "ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
