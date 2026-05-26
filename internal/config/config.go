// Package config loads runtime configuration from environment variables.
// Matches the Node shared/src/config.js contract so both backends share the
// same env vars during the parallel-run period.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL       string
	RedisURL          string
	MasterKey         []byte // raw 32-byte secret from base64
	JWTSecret         string
	RouterPort        int
	AdminPort         int
	WorkerInterval    int // seconds between worker ticks
	APIKeyCacheTTLSec int
	SuperAdminEmails  []string // lowercased
	LogLevel          string
}

var Loaded *Config

// Load reads env vars and validates required ones. Panics on missing required.
func Load() *Config {
	c := &Config{
		DatabaseURL:       required("DATABASE_URL"),
		RedisURL:          required("REDIS_URL"),
		JWTSecret:         required("JWT_SECRET"),
		RouterPort:        intOr("PORT", intOr("ROUTER_PORT", 30100)),
		AdminPort:         intOr("ADMIN_PORT", 30200),
		WorkerInterval:    intOr("WORKER_INTERVAL_SEC", 60),
		APIKeyCacheTTLSec: intOr("API_KEY_CACHE_TTL_SEC", 60),
		SuperAdminEmails:  csvLower("SUPER_ADMIN_EMAILS"),
		LogLevel:          strOr("LOG_LEVEL", "info"),
	}
	c.MasterKey = decodeMasterKey(required("CLOUD_MASTER_KEY"))
	Loaded = c
	return c
}

// IsSuperAdminEmail returns true if the email is configured as super-admin.
func (c *Config) IsSuperAdminEmail(email string) bool {
	if email == "" {
		return false
	}
	low := strings.ToLower(email)
	for _, e := range c.SuperAdminEmails {
		if e == low {
			return true
		}
	}
	return false
}

func required(name string) string {
	v := os.Getenv(name)
	if v == "" {
		panic(fmt.Sprintf("missing required env var: %s", name))
	}
	return v
}

func strOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func intOr(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func csvLower(name string) []string {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.ToLower(strings.TrimSpace(p)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func decodeMasterKey(s string) []byte {
	// base64 std (Node side uses openssl rand -base64 32 → 44-char trailing '=')
	dec, err := base64Decode(s)
	if err != nil || len(dec) != 32 {
		panic(fmt.Sprintf("CLOUD_MASTER_KEY must be base64 of 32 bytes (got %d bytes, err=%v)", len(dec), err))
	}
	return dec
}
