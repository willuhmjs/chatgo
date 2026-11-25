// Owned by BE-chat: fire-and-forget title generation for first exchange
// via Cfg.TaskModel (providers.Client.Complete).
package chat

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

const (
	titleFallbackRunes = 48 // used when no TaskModel is configured
	titleMaxRunes      = 60

	// defaultChatTitle mirrors the placeholder store.ChatsRepo.Create sets;
	// a chat carrying it still needs a generated title.
	defaultChatTitle = "New chat"
)

// titleEmitBudget is how long the stream waits (after Send's early kickoff)
// for a generated title before giving up on emitting the `title` Event.
// A var so tests can shrink it; production keeps 3s (spawn contract).
var titleEmitBudget = 3 * time.Second

// startTitle kicks off title generation for the chat's very first exchange
// and returns a channel carrying at most one title plus the kickoff time.
// It returns (nil, zero) when no title is needed (not the first message, or
// the chat already has a title). Generation never blocks the caller; the
// returned context is detached so client disconnects don't abort it.
func (s *Service) startTitle(ctx context.Context, chat *store.Chat, firstExchange bool, userMsg providers.ChatMessage) (<-chan string, time.Time) {
	if !firstExchange || (chat.Title != "" && chat.Title != defaultChatTitle) {
		return nil, time.Time{}
	}
	if strings.TrimSpace(userMsg.Content) == "" {
		return nil, time.Time{}
	}
	res := make(chan string, 1)
	start := time.Now()
	tctx, cancel := fireCtx(ctx)
	go func() {
		defer cancel() // the worker goroutine owns the detached context
		defer close(res)
		if title := s.generateTitle(tctx, userMsg.Content); title != "" {
			res <- title
		}
	}()
	return res, start
}

// waitTitle drains the kickoff result if it landed within titleEmitBudget of
// start; ok=false means "skip the title Event" (the caller persists it later).
func waitTitle(res <-chan string, start time.Time) (string, bool) {
	timer := time.NewTimer(titleEmitBudget - time.Since(start))
	defer timer.Stop()
	select {
	case title, ok := <-res:
		return title, ok && title != ""
	case <-timer.C:
		return "", false
	}
}

// generateTitle asks the TaskModel for a short title, falling back to the
// first 48 runes of the message when unset or on any upstream failure.
func (s *Service) generateTitle(ctx context.Context, firstUserMsg string) string {
	if providerID, modelID, ok := s.taskModel(ctx); ok {
		prov, err := s.taskProvider(ctx, providerID, modelID)
		if err != nil {
			slog.Warn("chat: task model provider unavailable", "error", err)
		} else {
			msgs := []providers.ChatMessage{
				{Role: "system", Content: "You name chat threads. Reply with only the title."},
				{Role: "user", Content: "3-5 word title for: " + firstUserMsg},
			}
			out, err := s.Prov.Complete(ctx, prov, modelID, msgs)
			if err != nil {
				slog.Warn("chat: title generation failed", "error", err)
			} else if t := cleanTitle(out); t != "" {
				return t
			}
		}
	}
	return fallbackTitle(firstUserMsg)
}

// applyTitle persists a generated title (re-reads the row so a concurrent
// model/params change is not clobbered). Returns false on any failure.
func (s *Service) applyTitle(ctx context.Context, chatID, title string) bool {
	chat, err := s.Store.Chats.Get(ctx, chatID)
	if err != nil {
		slog.Warn("chat: apply title failed", "chat_id", chatID, "error", err)
		return false
	}
	if chat.Title != "" && chat.Title != defaultChatTitle {
		return false // user renamed (or already titled) in the meantime
	}
	return s.setTitle(ctx, chat, title)
}

// setTitle writes title onto a freshly-read chat row. Kept separate from
// applyTitle so the regenerate path (which by definition replaces an
// existing title) shares the same re-read-then-write discipline.
func (s *Service) setTitle(ctx context.Context, chat *store.Chat, title string) bool {
	chat.Title = title
	if err := s.Store.Chats.Update(ctx, chat); err != nil {
		slog.Warn("chat: apply title failed", "chat_id", chat.ID, "error", err)
		return false
	}
	return true
}

// ErrTitleUnavailable: the task model is unset/refusing/failed, or the chat
// has no user message to name. Maps to HTTP 503 (same shape as suggestions).
var ErrTitleUnavailable = errors.New("chat: title generation unavailable")

// RegenerateTitle is the Open WebUI "Regenerate Title" action: ask the task
// model for a fresh title for the chat and persist it over whatever is
// there (unlike the auto-title path, which only fills the default). userID
// must own the chat (no-existence-leak rule as Send/SuggestNext). Unlike
// the auto-title fallback, a missing/failing task model is an ERROR here —
// clicking regenerate and silently getting a truncated message would be
// indistinguishable from a broken button.
func (s *Service) RegenerateTitle(ctx context.Context, chatID, userID string) (string, error) {
	chat, err := s.loadChat(ctx, userID, chatID)
	if err != nil {
		return "", err
	}
	providerID, modelID, ok := s.taskModel(ctx)
	if !ok {
		return "", ErrTitleUnavailable
	}
	prov, err := s.taskProvider(ctx, providerID, modelID)
	if err != nil {
		return "", ErrTitleUnavailable
	}
	msgs, err := s.Store.Messages.CurrentPath(ctx, chatID, chat.LeafID, suggestHistoryLimit)
	if err != nil {
		return "", err
	}
	seed := ""
	for i := range msgs {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Content) != "" {
			seed = msgs[i].Content
			break // the opening question names the thread best
		}
	}
	if seed == "" {
		return "", ErrTitleUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundTaskTimeout)
	defer cancel()
	out, err := s.Prov.Complete(ctx, prov, modelID, []providers.ChatMessage{
		{Role: "system", Content: "You name chat threads. Reply with only the title."},
		{Role: "user", Content: "3-5 word title for: " + seed},
	})
	if err != nil {
		slog.Warn("chat: regenerate title: task model call failed", "error", err)
		return "", ErrTitleUnavailable
	}
	title := cleanTitle(out)
	if title == "" {
		return "", ErrTitleUnavailable
	}
	if !s.setTitle(ctx, chat, title) {
		return "", ErrTitleUnavailable
	}
	return title, nil
}

// cleanTitle strips surrounding quotes/whitespace/periods, keeps the first
// line, and caps length.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "\"'`“”«» .。")
	if r := []rune(s); len(r) > titleMaxRunes {
		s = string(r[:titleMaxRunes])
	}
	return s
}

// fallbackTitle is the TaskModel-unset fallback: first 48 runes, whitespace
// collapsed.
func fallbackTitle(msg string) string {
	fields := strings.FieldsFunc(msg, unicode.IsSpace)
	s := strings.Join(fields, " ")
	if r := []rune(s); len(r) > titleFallbackRunes {
		s = string(r[:titleFallbackRunes])
	}
	return s
}
