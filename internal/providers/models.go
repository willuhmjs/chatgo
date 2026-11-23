package providers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// rawModel is one entry of a GET {base}/models response. Field sets vary
// wildly across gateways, so everything is optional and unknown fields are
// ignored.
type rawModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	// Some gateways advertise the context window explicitly.
	ContextLength int `json:"context_length"`
	ContextLen    int `json:"context_len"`
	MaxModelLen   int `json:"max_model_len"`      // vLLM
	MaxContextTok int `json:"max_context_tokens"` // SGLang-style
}

// parseModelList maps a GET {base}/models JSON body into []Model.
// Tolerates the {"data":[…]} wrapper and bare-array shapes, missing display
// fields, and duplicate ids (first occurrence wins). Pure function.
func parseModelList(body []byte) ([]Model, error) {
	var entries []rawModel
	var wrap struct {
		Data []rawModel `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.Data != nil {
		entries = wrap.Data
	} else {
		var arr []rawModel
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, fmt.Errorf("providers: unexpected /models response shape: %w", err)
		}
		entries = arr
	}

	out := make([]Model, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, m := range entries {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		display := firstNonEmpty(m.DisplayName, m.Name)
		if display == "" {
			display = humanizeModelID(id)
		}
		out = append(out, Model{ID: id, Display: display, ContextLen: modelContextLen(id, m)})
	}
	return out, nil
}

// modelContextLen prefers explicit gateway fields, then id-hint heuristics.
func modelContextLen(id string, m rawModel) int {
	for _, v := range []int{m.ContextLength, m.ContextLen, m.MaxModelLen, m.MaxContextTok} {
		if v > 0 {
			return v
		}
	}
	return guessContextLen(id)
}

// ctxHintRe matches context-window suffixes in model ids — the "16k",
// "128k", "1m" style used by vLLM/llama.cpp/OpenRouter naming
// ("gpt-4-32k", "qwen2.5-coder-1m", "llama-3.1-8b-instruct-128k"). The \b
// tail keeps size classes like "70b" out, and digits need to abut the k/m
// directly, so dates ("2024-07-18") and "8x7b" never match.
var ctxHintRe = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)([km])\b`)

// guessContextLen heuristically derives a context length from model-id
// hints (k = ×1000, m = ×1,000,000). Returns 0 (unknown) when no plausible
// hint is present or the result is out of sane bounds.
func guessContextLen(id string) int {
	m := ctxHintRe.FindStringSubmatch(strings.ToLower(id))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := 1000.0
	if m[2] == "m" {
		mult = 1_000_000
	}
	v := int(n * mult)
	if v < 1024 || v > 100_000_000 {
		return 0 // e.g. "1k-tokenizer" noise or absurd values
	}
	return v
}

// humanizeModelID derives a display name from an id with no metadata
// ("openai/gpt-4o-mini" → "Gpt 4o mini"). Good enough for a cache list.
func humanizeModelID(id string) string {
	s := id
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(s)
	if s == "" {
		return id
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
