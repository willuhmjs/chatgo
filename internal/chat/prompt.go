// Owned by BE-chat: system prompt assembly (base persona + SearchCtx splice),
// history loading and context-window compaction (contract §Chat service).
package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

const (
	// defaultSystemPrompt is used unless the settings table overrides it.
	// Deliberately brand-free: the assistant's identity belongs to the model
	// (or to the admin-configured prompt), not to a hardcoded string here.
	defaultSystemPrompt = "You are a helpful and knowledgeable assistant. " +
		"Answer clearly, admit uncertainty, and use Markdown formatting when it helps."

	settingSystemPrompt = "system_prompt"
	settingTaskModel    = "task_model"

	// defaultContextLen is assumed when the model cache has no context_len.
	defaultContextLen = 8192

	// Compaction thresholds vs the model context window (contract §Chat service).
	compactionTrigger = 0.70 // compact when estimate exceeds this fraction
	compactionBudget  = 0.60 // keep the kept-window under this fraction

	// backgroundTaskTimeout bounds fire-and-forget work (titles, summaries,
	// final persistence) once detached from the request context.
	backgroundTaskTimeout = 60 * time.Second
)

// estimateTokens is the contract's chars/4 heuristic (no tiktoken).
func estimateTokens(msgs []providers.ChatMessage) int {
	var n int
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n / 4
}

// systemPrompt returns the base persona; settings key "system_prompt" wins.
// override (chats.params_json."system") beats both — the per-chat system
// prompt the user set in the chat's settings drawer.
func (s *Service) systemPrompt(ctx context.Context, opts SendOpts, override string) string {
	base := func() string {
		if override != "" {
			return override
		}
		v, err := s.Store.Settings.Get(ctx, settingSystemPrompt)
		if err != nil {
			slog.Warn("chat: read system_prompt failed", "error", err)
			return defaultSystemPrompt
		}
		if v != nil && strings.TrimSpace(*v) != "" {
			return *v
		}
		return defaultSystemPrompt
	}()
	return base + userContext(opts)
}

// userContext renders the per-turn clock/user anchor appended to every
// system prompt: UTC date always; the browser timezone and (opt-in) coarse
// location when the client shared them. Client-supplied values are treated
// as hostile input: the timezone must parse as a real IANA zone (this also
// neutralises prompt injection via the body field), coordinates must be in
// range and are rounded to ~1 km — the model gets "Singapore", not a home
// address.
func userContext(opts SendOpts) string {
	now := time.Now().UTC()
	var b strings.Builder
	b.WriteString("\n\nCurrent date: " + now.Format("2006-01-02") + " (" + now.Weekday().String() + ", UTC).")
	if opts.Timezone != "" && len(opts.Timezone) <= 64 {
		if loc, err := time.LoadLocation(opts.Timezone); err == nil {
			local := now.In(loc)
			b.WriteString(fmt.Sprintf(" User timezone: %s (their local date/time: %s).",
				opts.Timezone, local.Format("2006-01-02 15:04")))
		}
	}
	if opts.HasLoc && opts.LocLat >= -90 && opts.LocLat <= 90 && opts.LocLon >= -180 && opts.LocLon <= 180 {
		b.WriteString(fmt.Sprintf(" User approximate location (shared by their browser): %.2f, %.2f (lat, lon — use only when relevant).",
			opts.LocLat, opts.LocLon))
	}
	return b.String()
}

// modelContextLen looks up the chat model's cached context_len, falling back
// to defaultContextLen when unknown.
func (s *Service) modelContextLen(ctx context.Context, chat *store.Chat) int {
	models, err := s.Store.Models.ForProvider(ctx, chat.ProviderID)
	if err != nil {
		slog.Warn("chat: model lookup failed", "error", err)
		return defaultContextLen
	}
	for _, m := range models {
		if m.ModelID == chat.ModelID && m.ContextLen > 0 {
			return m.ContextLen
		}
	}
	return defaultContextLen
}

// history returns the conversation the next turn is built on. With leafID set
// it is the branch chain root→leaf (Open WebUI parentId-tree semantics); with
// leafID == "" it degrades to the newest 500 rows (linear legacy reads and
// truncate-mode flows whose leaf was just deleted). The returned `total` is
// that same list; `effective` is everything after the newest summary's
// up_to_message_id — or the whole list when no summary is on this branch
// (a summary from a sibling branch describes content this path doesn't have,
// so it is dropped rather than duplicated).
func (s *Service) history(ctx context.Context, chatID, leafID string) (total, effective []*store.Message, summary *store.Summary, err error) {
	total, err = s.Store.Messages.CurrentPath(ctx, chatID, leafID, 500)
	if err != nil {
		return nil, nil, nil, err
	}
	summary, err = s.latestSummary(ctx, chatID)
	if err != nil {
		return nil, nil, nil, err
	}
	effective = total
	if summary != nil && summary.UpToMessageID != "" {
		if idx := indexOfMessage(total, summary.UpToMessageID); idx >= 0 {
			effective = total[idx+1:]
		} else {
			summary = nil // summary belongs to another branch — don't inject or duplicate
		}
	}
	return total, effective, summary, nil
}

func indexOfMessage(msgs []*store.Message, id string) int {
	for i, m := range msgs {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// assemblePrompt builds the upstream message list:
//
//	[system, summary?, history…]
//
// compacting when the estimate exceeds compactionTrigger of contextLen:
// keep system + summary + first 2 history messages + the latest N that fit
// under compactionBudget. Images are attached to the live user message.
// compacted reports whether the raw history blew the trigger (summarize cue).
func assemblePrompt(system string, summary *store.Summary, history []*store.Message, contextLen int, liveUserMsgID string, images []string) ([]providers.ChatMessage, bool) {
	head := []providers.ChatMessage{{Role: "system", Content: system}}
	if summary != nil && strings.TrimSpace(summary.Text) != "" {
		head = append(head, providers.ChatMessage{
			Role:    "system",
			Content: "Summary of the earlier conversation:\n" + summary.Text,
		})
	}

	body := make([]providers.ChatMessage, 0, len(history))
	for _, m := range history {
		cm := providers.ChatMessage{Role: m.Role, Content: m.Content}
		if liveUserMsgID != "" && m.ID == liveUserMsgID && len(images) > 0 {
			cm.Images = images
		}
		body = append(body, cm)
	}

	if contextLen <= 0 || estimateTokens(head)+estimateTokens(body) <= int(float64(contextLen)*compactionTrigger) {
		return append(head, body...), false
	}

	// Keep head + first 2 messages; fill the tail newest-first under budget.
	var kept []providers.ChatMessage
	n := min(2, len(body))
	kept = append(kept, body[:n]...)
	budget := int(float64(contextLen)*compactionBudget) - estimateTokens(head) - estimateTokens(kept)
	used := 0
	tailStart := len(body)
	for i := len(body) - 1; i >= n; i-- {
		cost := len(body[i].Content) / 4
		if used+cost > budget && tailStart < len(body) {
			break // keep at least the latest message
		}
		used += cost
		tailStart = i
	}
	kept = append(kept, body[tailStart:]...)
	return append(head, kept...), true
}

// taskModel resolves the title/summary model: settings key "task_model"
// overrides Cfg.TaskModel; format "providerID:modelID".
func (s *Service) taskModel(ctx context.Context) (providerID, modelID string, ok bool) {
	spec := s.Cfg.TaskModel
	if v, err := s.Store.Settings.Get(ctx, settingTaskModel); err != nil {
		slog.Warn("chat: read task_model setting failed", "error", err)
	} else if v != nil && strings.TrimSpace(*v) != "" {
		spec = strings.TrimSpace(*v)
	}
	return splitTaskModel(spec)
}

func splitTaskModel(spec string) (providerID, modelID string, ok bool) {
	i := strings.IndexByte(spec, ':')
	if i <= 0 || i == len(spec)-1 {
		return "", "", false
	}
	return spec[:i], spec[i+1:], true
}

// taskProvider fetches + decrypts the TaskModel's provider row and applies
// the SAME kill-switch policy the send path enforces (service.go step 7/7b):
// an admin's provider disable or per-model disable must stop ALL traffic
// immediately (SECREV4 F-1/M3; the S2-1 send-path fix never reached these
// utility paths, so every new chat kept spending a disabled task model's
// key on titles, and long chats on summaries).
//
// Callers (title.go, summarize.go) treat every error exactly like the
// KeyUnreadable one already does — log a warning, fall back to the plain
// title / skip the summary. A background utility never fails the user's
// turn.
func (s *Service) taskProvider(ctx context.Context, providerID, modelID string) (providers.Provider, error) {
	row, err := s.Store.Providers.Get(ctx, providerID)
	if err != nil {
		return providers.Provider{}, err
	}
	if !row.Enabled {
		return providers.Provider{}, errors.New(errMsgProviderDisabled)
	}
	if row.KeyUnreadable {
		// Same law as the send path: never fire upstream with an empty key
		// and blame the provider for the resulting 401.
		return providers.Provider{}, errors.New(errMsgKeyUnreadable)
	}
	// Per-model kill: gate only when the models cache carries an explicit
	// disabled row for the task model. Task models are pinned by id in
	// admin settings and may legitimately never appear in a synced catalog
	// (sync may also drop rows later), so an UNCACHED pair is no verdict —
	// only enabled = 0 means "the admin killed this model".
	found, enabled, err := s.Store.Models.ModelState(ctx, providerID, modelID)
	if err != nil {
		slog.Warn("chat: task model cache lookup failed", "provider_id", providerID, "model_id", modelID, "error", err)
		return providers.Provider{}, err // fail-closed: never dispatch blind
	}
	if found && !enabled {
		return providers.Provider{}, errors.New(errMsgModelDisabled)
	}
	return providerFromRow(row), nil
}
