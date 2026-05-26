// Package errs defines the typed application errors and their HTTP mappings.
// Mirrors Node shared/src/errors.js.
package errs

import "fmt"

// AppError is the base interface every typed error implements.
type AppError struct {
	Code    string         // machine code, e.g. "validation_error"
	Message string         // human-readable
	Status  int            // HTTP status
	Meta    map[string]any // optional extra detail
}

func (e *AppError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Constructors — these mirror the Node class hierarchy.

func Validation(msg string, meta ...map[string]any) *AppError {
	return &AppError{Code: "validation_error", Message: msg, Status: 400, Meta: firstMeta(meta)}
}

func Auth(msg string, meta ...map[string]any) *AppError {
	return &AppError{Code: "auth_error", Message: msg, Status: 401, Meta: firstMeta(meta)}
}

func Forbidden(msg string, meta ...map[string]any) *AppError {
	return &AppError{Code: "forbidden", Message: msg, Status: 403, Meta: firstMeta(meta)}
}

func NotFound(msg string, meta ...map[string]any) *AppError {
	return &AppError{Code: "not_found", Message: msg, Status: 404, Meta: firstMeta(meta)}
}

func RateLimit(msg string, meta ...map[string]any) *AppError {
	return &AppError{Code: "rate_limit_exceeded", Message: msg, Status: 429, Meta: firstMeta(meta)}
}

func Upstream(msg string, meta ...map[string]any) *AppError {
	return &AppError{Code: "upstream_error", Message: msg, Status: 502, Meta: firstMeta(meta)}
}

func NoAccountAvailable(meta ...map[string]any) *AppError {
	return &AppError{
		Code:    "no_account_available",
		Message: "No upstream account available (all in cooldown or none configured)",
		Status:  503, Meta: firstMeta(meta),
	}
}

func Internal(msg string) *AppError {
	return &AppError{Code: "internal_error", Message: msg, Status: 500}
}

func firstMeta(m []map[string]any) map[string]any {
	if len(m) > 0 {
		return m[0]
	}
	return nil
}
