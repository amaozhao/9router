// Package combo resolves a logical `model` string into an ordered list of
// (provider, upstreamModel) attempts. Three shapes supported, matching the
// Node services/combo.js precisely:
//
//	combo:<slug>      → look up combos.nodes for the tenant
//	<provider>:<m>    → single attempt
//	<bare model>      → heuristic provider mapping
package combo

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/amaozhao/lazirouter/internal/db"
	"github.com/amaozhao/lazirouter/internal/errs"
)

// Attempt is one node in the fallback chain.
type Attempt struct {
	Provider      string
	UpstreamModel string
	ConnectionID  *int64
	Step          int
	SourceCombo   string
}

type comboNode struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	ConnectionID *int64 `json:"connection_id,omitempty"`
	Weight       int    `json:"weight,omitempty"`
}

var gptRe = regexp.MustCompile(`^(gpt-|o\d|chatgpt-)`)

// Resolve returns the attempts in order, or a ValidationError.
func Resolve(ctx context.Context, tenantID int64, modelInput string) ([]Attempt, error) {
	if modelInput == "" {
		return nil, errs.Validation("model must be a non-empty string")
	}

	if strings.HasPrefix(modelInput, "combo:") {
		slug := modelInput[6:]
		var nodesJSON []byte
		err := db.Pool().QueryRow(ctx, `
			SELECT nodes FROM combos
			WHERE tenant_id = $1 AND slug = $2 AND enabled = TRUE LIMIT 1
		`, tenantID, slug).Scan(&nodesJSON)
		if err != nil {
			return nil, errs.Validation("Unknown combo: "+slug,
				map[string]any{"hint": "create it via POST /api/combos"})
		}
		var nodes []comboNode
		if err := json.Unmarshal(nodesJSON, &nodes); err != nil || len(nodes) == 0 {
			return nil, errs.Validation("Combo " + slug + " has no nodes")
		}
		out := make([]Attempt, len(nodes))
		for i, n := range nodes {
			out[i] = Attempt{
				Provider:      strings.ToLower(n.Provider),
				UpstreamModel: n.Model,
				ConnectionID:  n.ConnectionID,
				Step:          i,
				SourceCombo:   slug,
			}
		}
		return out, nil
	}

	// explicit provider:model
	if idx := strings.Index(modelInput, ":"); idx > 0 && idx < len(modelInput)-1 {
		return []Attempt{{
			Provider:      strings.ToLower(modelInput[:idx]),
			UpstreamModel: modelInput[idx+1:],
			Step:          0,
		}}, nil
	}

	// heuristic — subscription-first defaults for claude/gpt/o-series
	provider := "openai"
	switch {
	case strings.HasPrefix(modelInput, "glm-"):
		provider = "glm"
	case strings.HasPrefix(modelInput, "deepseek-"):
		provider = "deepseek"
	case strings.HasPrefix(modelInput, "claude-"):
		provider = "claude"
	case strings.HasPrefix(modelInput, "gemini-"):
		provider = "gemini"
	case strings.HasPrefix(modelInput, "mock-"):
		provider = "mock"
	case gptRe.MatchString(modelInput):
		provider = "codex"
	}
	return []Attempt{{Provider: provider, UpstreamModel: modelInput, Step: 0}}, nil
}
