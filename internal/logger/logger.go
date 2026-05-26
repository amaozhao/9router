// Package logger wraps log/slog to emit JSON logs whose shape mirrors the
// Node pino output the previous backend produced: {ts, level, msg, svc, ...}.
package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

var root *slog.Logger

// Init sets up the global JSON logger. `svc` becomes a constant field on every line.
func Init(svc, level string) {
	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// rename "time" → "ts" to match pino
			if a.Key == slog.TimeKey {
				a.Key = "ts"
			}
			return a
		},
	}
	h := slog.NewJSONHandler(os.Stdout, opts)
	root = slog.New(h).With("svc", svc)
}

// L returns the global logger; must be called after Init.
func L() *slog.Logger {
	if root == nil {
		Init("unknown", "info")
	}
	return root
}

// With adds permanent fields to a child logger.
func With(args ...any) *slog.Logger {
	return L().With(args...)
}

// Info / Warn / Error are short-hands matching Node usage.
func Info(msg string, args ...any)  { L().Info(msg, args...) }
func Warn(msg string, args ...any)  { L().Warn(msg, args...) }
func Error(msg string, args ...any) { L().Error(msg, args...) }
func Debug(msg string, args ...any) { L().Debug(msg, args...) }

// LogContext is a no-op placeholder — when we want per-request context fields we
// thread them through the *slog.Logger explicitly instead of using context.
func _useCtx(ctx context.Context) {} //nolint:unused

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
