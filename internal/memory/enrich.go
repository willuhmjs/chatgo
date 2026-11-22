// Chat-turn enrichment (FEATURES-V2 wave 2): injects the user's enabled
// memories as an "About the user:" extra-system block. Exposed as a plain
// function; the integrator binds it into internal/chat's Enricher registry
// (see the snippet on Enrich).

package memory

import (
	"context"
	"strings"
	"unicode/utf8"

	"chatgo/internal/store"
)

// Source is one citation surfaced alongside the enrichment, shaped exactly
// like chat.SourceItem / the SSE `sources` items so the integrator maps it
// 1:1. Memory enrichment cites nothing today (the block is the user's own
// declared facts, not external material), so Enrich returns no sources;
// the type exists so the contract's ([]Source, extraSystem, error) shape
// stays honest and future relevance citations need no re-binding.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Enrich loads the user's enabled memories (newest-first) and renders the
// injection block documented in the feature spec:
//
//	About the user:
//	- <newest>
//	- …
//
// Lines are appended newest-first until the block reaches MaxInjectRunes
// (2000) characters; the newest memory is always included (the 500-char
// content cap guarantees it fits). No enabled memories → empty
// extraSystem (a clean no-op). userMsg is accepted for Enricher-shape
// compatibility; v1 injects all enabled memories regardless of the turn
// (relevance filtering is a later wave).
//
// Integrator binding (the exact lines, per FEATURES-V2 §Coordination law):
//
//	mux.HandleFunc wiring: memory.Register(mux, &memory.Deps{Cfg: cfg, Store: st, Auth: a, Providers: pc})
//	                       // Register runs memory.Init(st) itself
//	chat binding:          chatSvc.RegisterEnricher("memory", func(ctx context.Context,
//	                           c *store.Chat, um providers.ChatMessage) ([]chat.SourceItem, string, error) {
//	                           _, extra, err := memory.Enrich(ctx, st, c.UserID, um.Content)
//	                           return nil, extra, err // Enrich cites nothing; map []memory.Source 1:1 if it ever does
//	                           })
//
// The userID resolver is c.UserID (the chat owner, loaded by the chat
// service), so enriching a shared/proxied turn still reads the right
// user's memories.
func Enrich(ctx context.Context, st *store.Store, userID, userMsg string) ([]Source, string, error) {
	if st == nil || userID == "" {
		return nil, "", nil
	}
	contents, err := enabledContents(ctx, st.DB, userID)
	if err != nil {
		return nil, "", err
	}
	block := injectBlock(contents)
	if block == "" {
		return nil, "", nil
	}
	return nil, block, nil
}

// injectBlock renders contents (already newest-first) as the injection
// block, cutting off at MaxInjectRunes. Exported-ish only for tests via
// package scope.
func injectBlock(contents []string) string {
	nonEmpty := make([]string, 0, len(contents))
	for _, c := range contents {
		if c = strings.Join(strings.Fields(c), " "); c != "" {
			nonEmpty = append(nonEmpty, c)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(injectHeader)
	b.WriteString("\n")
	used := utf8.RuneCountInString(injectHeader) + 1
	for i, c := range nonEmpty {
		cost := utf8.RuneCountInString("- "+c) + 1 // "- " … + newline
		if used+cost > MaxInjectRunes {
			if i > 0 {
				break // budget exhausted; older memories simply do not fit
			}
			// Defensive: the newest line alone over the cap is unreachable
			// through the API (500-char content rule), but honor the hard
			// 2000-char block cap by truncating it instead of overflowing.
			fit := MaxInjectRunes - used - 3 // "- " prefix + trailing newline
			if fit <= 0 {
				break
			}
			c = string([]rune(c)[:fit])
		}
		b.WriteString("- ")
		b.WriteString(c)
		b.WriteString("\n")
		used += 2 + utf8.RuneCountInString(c) + 1
	}
	return strings.TrimSuffix(b.String(), "\n")
}
