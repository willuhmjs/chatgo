// Owned by BE-chat: system prompt assembly (base persona + SearchCtx splice),
// history loading and context-window compaction (contract §Chat service).
package chat

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

const (
	// defaultSystemPrompt is used unless the settings table overrides it.
	defaultSystemPrompt = "You are ChatGo, a helpful and knowledgeable assistant. " +
		"Answer clearly, admit uncertainty, and use Markdown formatting when it helps."

	settingSystemPrompt = "system_prompt"
	settingTaskModel    = "task_model"

	// defaultContextLen is assumed when the model cache has no context_len.
	defaultContextLen = 8192

	// Compaction thresholds vs the model context window (contract §Chat service).
	compactionTrigger = 0.70 // compact when estimate exceeds this fraction
	compactionBudget  = 0.60 // keep the kept-window under this fraction

	searchMaxResults = 5

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
func (s *Service) systemPrompt(ctx context.Context) string {
	v, err := s.Store.Settings.Get(ctx, settingSystemPrompt)
	if err != nil {
		slog.Warn("chat: read system_prompt failed", "error", err)
		return defaultSystemPrompt
	}
	if v != nil && strings.TrimSpace(*v) != "" {
		return *v
	}
	return defaultSystemPrompt
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

// history returns every message of the chat (total, ascending), the effective
// history (everything after the newest summary's up_to_message_id) and that
// summary (nil when none).
func (s *Service) history(ctx context.Context, chatID string) (total, effective []*store.Message, summary *store.Summary, err error) {
	total, err = s.Store.Messages.List(ctx, chatID, 500, "")
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

// taskProvider fetches + decrypts the TaskModel's provider row.
func (s *Service) taskProvider(ctx context.Context, providerID string) (providers.Provider, error) {
	row, err := s.Store.Providers.Get(ctx, providerID)
	if err != nil {
		return providers.Provider{}, err
	}
	return providerFromRow(row), nil
}
