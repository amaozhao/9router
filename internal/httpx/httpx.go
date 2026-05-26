// Package httpx provides tiny HTTP helpers used by all routes.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/amaozhao/lazirouter/internal/errs"
)

const MaxBodyBytes = 5 * 1024 * 1024

// ReadJSON decodes the request body into v (must be a pointer). 5 MiB cap.
func ReadJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, MaxBodyBytes)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errs.Validation("Empty body")
		}
		return errs.Validation("Invalid JSON body: " + err.Error())
	}
	return nil
}

// ReadJSONMap is the loose variant used when we don't yet have a target struct.
func ReadJSONMap(r *http.Request) (map[string]any, error) {
	var m map[string]any
	if err := ReadJSON(r, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

// WriteJSON writes status + JSON body. Sets content-type for you.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteError serialises an AppError (or wraps a generic error as 500).
func WriteError(w http.ResponseWriter, err error) {
	var ae *errs.AppError
	if errors.As(err, &ae) {
		WriteJSON(w, ae.Status, map[string]any{
			"error": map[string]any{"code": ae.Code, "message": ae.Message, "meta": ae.Meta},
		})
		return
	}
	WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]any{"code": "internal_error", "message": err.Error()},
	})
}

// DefaultBaseURL returns the well-known upstream base URL for a provider, or
// empty string if there is no public default and the connection must supply
// `metadata.base_url`.
func DefaultBaseURL(provider string) string {
	switch provider {
	case "openai":
		return "https://api.openai.com"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case "glm":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "deepseek":
		return "https://api.deepseek.com"
	case "minimax":
		return "https://api.minimaxi.com"
	case "anthropic":
		return "https://api.anthropic.com"
	}
	return ""
}

// BearerOrXAPIKey extracts the API key from Authorization: Bearer ... or
// x-api-key header (Anthropic / OpenAI SDK conventions).
func BearerOrXAPIKey(r *http.Request) string {
	if a := r.Header.Get("Authorization"); len(a) > 7 && a[:7] == "Bearer " {
		return a[7:]
	}
	return r.Header.Get("x-api-key")
}

// StringFrom safely fetches a string field from a generic map[string]any.
// Returns "" if missing or wrong type.
func StringFrom(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}
