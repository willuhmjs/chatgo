// Owned by BE-chat: rolling summaries (chat > 40 msgs → keep last summary +
// last 30 unsummarized) persisted via store summaries table.
package chat

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"chatgo/internal/providers"
	"chatgo/internal/store"

	"github.com/google/uuid"
)

const (
	summarizeMsgThreshold = 40 // chat message count that triggers a summary
	summarizeKeepRecent   = 30 // messages left unsummarized (contract §Chat service)
)

const summarizeInstruction = "Summarize the conversation so far into a compact, factual brief " +
	"(key facts, decisions, open questions, names). Keep it under 300 words. " +
	"This text replaces the summarized messages as prompt context."

// CONTRACT-DEVIATION: docs/CONTRACTS.md §Store lists no Summaries repo and
// internal/store has no such methods/field, but the `summaries` table and
// store.Summary row struct exist and the contract assigns summary writing to
// this package. Until BE adds Summaries.Latest/Upsert, we query the frozen
// schema directly through the exported Store.DB handle (parameterized only).

// latestSummary returns the newest summaries row for the chat (nil when none).
func (s *Service) latestSummary(ctx context.Context, chatID string) (*store.Summary, error) {
	row := s.Store.DB.QueryRowContext(ctx,
		`SELECT id, chat_id, up_to_message_id, text, created_at
		 FROM summaries WHERE chat_id = ?1 ORDER BY created_at DESC, id DESC LIMIT 1`, chatID)
	var sum store.Summary
	err := row.Scan(&sum.ID, &sum.ChatID, &sum.UpToMessageID, &sum.Text, &sum.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

// saveSummary replaces the chat's summary (keep-last semantics).
func (s *Service) saveSummary(ctx context.Context, chatID, upToMessageID, text string) error {
	if _, err := s.Store.DB.ExecContext(ctx, `DELETE FROM summaries WHERE chat_id = ?1`, chatID); err != nil {
		return err
	}
	_, err := s.Store.DB.ExecContext(ctx,
		`INSERT INTO summaries (id, chat_id, up_to_message_id, text, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5)`,
		uuid.NewString(), chatID, upToMessageID, text, timeNowUnix())
	return err
}

// maybeSummarize decides (cheaply) whether a rolling summary is due and
// fires the TaskModel call on a worker goroutine with its own detached
// context (see fireCtx), which the WORKER cancels when it returns. Triggers:
// chat crossing summarizeMsgThreshold messages, or prompt compaction kicking
// in. The stream is never blocked here; parent may be the request context.
func (s *Service) maybeSummarize(parent context.Context, chatID string, total, effective []*store.Message, prev *store.Summary, compacted bool) {
	overCount := len(total) > summarizeMsgThreshold
	if !overCount && !compacted {
		return
	}
	sctx, cancel := fireCtx(parent)
	go func() {
		defer cancel() // the worker goroutine owns the detached context
		s.summarizeTurn(sctx, chatID, effective, prev, compacted)
	}()
}

// summarizeTurn is the detached half: window selection, TaskModel
// resolution and the upstream call.
func (s *Service) summarizeTurn(ctx context.Context, chatID string, effective []*store.Message, prev *store.Summary, compacted bool) {
	// Summarize everything except the newest summarizeKeepRecent messages
	// (the effective window already excludes what an older summary covered).
	end := len(effective) - summarizeKeepRecent
	if end < 1 {
		// Compaction with a short chat: still fold all but the latest turn.
		if compacted && len(effective) >= 2 {
			end = len(effective) - 1
		} else {
			return
		}
	}
	window := effective[:end]
	upTo := window[len(window)-1].ID
	prevText := ""
	if prev != nil {
		prevText = prev.Text
	}
	msgs := make([]providers.ChatMessage, 0, len(window))
	if prevText != "" {
		msgs = append(msgs, providers.ChatMessage{Role: "system", Content: "Earlier summary:\n" + prevText})
	}
	for _, m := range window {
		msgs = append(msgs, providers.ChatMessage{Role: m.Role, Content: m.Content})
	}

	providerID, modelID, ok := s.taskModel(ctx)
	if !ok {
		return // no TaskModel → no summarize (fallback per spawn contract)
	}
	prov, err := s.taskProvider(ctx, providerID)
	if err != nil {
		slog.Warn("chat: summarize provider unavailable", "error", err)
		return
	}
	s.summarize(ctx, prov, modelID, chatID, upTo, msgs)
}

// summarize runs the non-streaming TaskModel call and upserts the result.
func (s *Service) summarize(ctx context.Context, prov providers.Provider, modelID, chatID, upTo string, msgs []providers.ChatMessage) {
	msgs = append([]providers.ChatMessage{{Role: "system", Content: summarizeInstruction}}, msgs...)
	text, err := s.Prov.Complete(ctx, prov, modelID, msgs)
	if err != nil {
		slog.Warn("chat: summarize failed", "chat_id", chatID, "error", err)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if err := s.saveSummary(ctx, chatID, upTo, text); err != nil {
		slog.Warn("chat: save summary failed", "chat_id", chatID, "error", err)
	}
}

func timeNowUnix() int64 { return time.Now().Unix() }
