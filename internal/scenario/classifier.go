// Package scenario classifies an incoming chat/messages body into a
// fixed-vocabulary scenario and resolves the per-tenant target model.
package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
	"github.com/amaozhao/lazirouter/internal/redisx"
)

// Scenario enum — exported as strings to keep parity with the Node side.
const (
	Default     = "default"
	Think       = "think"
	LongContext = "long_context"
	Vision      = "vision"
	ToolUse     = "tool_use"
	Web         = "web"
)

const longContextCharThreshold = 64_000 * 4

var webToolNameRe = regexp.MustCompile(`(?i)search|web|browse`)

// Classify is pure: no I/O. `body` is the parsed request body (either OpenAI
// chat/completions or Anthropic /v1/messages — both shapes handled).
func Classify(body map[string]any) string {
	if hasWebTool(body) {
		return Web
	}
	if hasTools(body) {
		return ToolUse
	}
	if hasImage(body) {
		return Vision
	}
	if estimateChars(body) > longContextCharThreshold {
		return LongContext
	}
	if isThink(body) {
		return Think
	}
	return Default
}

func hasTools(body map[string]any) bool {
	t, ok := body["tools"].([]any)
	return ok && len(t) > 0
}

func hasWebTool(body map[string]any) bool {
	tools, ok := body["tools"].([]any)
	if !ok {
		return false
	}
	for _, t := range tools {
		tt, _ := t.(map[string]any)
		if tt == nil {
			continue
		}
		// either {function: {name: ...}} (OpenAI) or {name: ...} (Anthropic)
		var name string
		if fn, _ := tt["function"].(map[string]any); fn != nil {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			name, _ = tt["name"].(string)
		}
		if webToolNameRe.MatchString(name) {
			return true
		}
	}
	return false
}

func hasImage(body map[string]any) bool {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return false
	}
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		parts, _ := mm["content"].([]any)
		for _, p := range parts {
			pp, _ := p.(map[string]any)
			if pp == nil {
				continue
			}
			if pp["type"] == "image" || pp["type"] == "image_url" {
				return true
			}
			if src, _ := pp["source"].(map[string]any); src != nil {
				if mt, _ := src["media_type"].(string); strings.HasPrefix(mt, "image/") {
					return true
				}
			}
		}
	}
	return false
}

func estimateChars(body map[string]any) int {
	total := 0
	if s, ok := body["system"].(string); ok {
		total += len(s)
	}
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		c := mm["content"]
		if s, ok := c.(string); ok {
			total += len(s)
		} else {
			b, _ := json.Marshal(c)
			total += len(b)
		}
	}
	return total
}

func isThink(body map[string]any) bool {
	if re, ok := body["reasoning_effort"].(string); ok && (re == "high" || re == "max") {
		return true
	}
	if think, ok := body["thinking"].(map[string]any); ok {
		if en, _ := think["enabled"].(bool); en {
			return true
		}
	}
	return false
}

// --- Per-tenant routing ---

const routingCacheTTL = 30 * time.Second

func routingKey(tenantID int64) string {
	return fmt.Sprintf("tenant_routing:%d", tenantID)
}

// InvalidateRouting drops the Redis cache (call from admin PUT/DELETE).
func InvalidateRouting(ctx context.Context, tenantID int64) error {
	return redisx.C().Del(ctx, routingKey(tenantID)).Err()
}

// ResolveTarget returns the configured target (combo:slug / provider:model)
// for the scenario. Falls back to 'default' target; errors if neither is set.
func ResolveTarget(ctx context.Context, tenantID int64, scn string) (string, error) {
	m, err := loadRouting(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if v, ok := m[scn]; ok {
		return v, nil
	}
	if scn != Default {
		if v, ok := m[Default]; ok {
			return v, nil
		}
	}
	return "", errs.Validation("租户未配置 default 自动路由目标，请在管理后台 /api/routing/default 设置")
}

func loadRouting(ctx context.Context, tenantID int64) (map[string]string, error) {
	rc := redisx.C()
	if cached, err := rc.Get(ctx, routingKey(tenantID)).Result(); err == nil && cached != "" {
		var out map[string]string
		if err := json.Unmarshal([]byte(cached), &out); err == nil {
			return out, nil
		}
	}
	rows, err := db.Pool().Query(ctx,
		`SELECT scenario, target FROM tenant_routing WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var s, t string
		if err := rows.Scan(&s, &t); err != nil {
			return nil, err
		}
		out[s] = t
	}
	buf, _ := json.Marshal(out)
	_ = rc.Set(ctx, routingKey(tenantID), buf, routingCacheTTL).Err()
	return out, nil
}
