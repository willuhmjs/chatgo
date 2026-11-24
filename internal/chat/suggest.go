// Owned by BE-chat: "suggest next message" (Open WebUI-style suggestion
// model). The user clicks the sparkle and the instance task model proposes
// short questions they might plausibly ask next. Unlike titles/summaries —
// which must never fail the user's turn and fall back silently — this call
// IS the user's request, so every failure surfaces as one of the typed
// errors below and the handler maps them to a clean 503. Task-model
// resolution and admin gating reuse the exact same machinery as
// titles/summaries (taskModel / taskProvider in prompt.go); no new provider
// path, no caching, and every input/output size is bounded by a constant.
package chat

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// Typed SuggestNext failures. Both map to HTTP 503 in apichat: the task
// model is an optional, admin-configured dependency, so "unset / disabled /
// upstream unhappy / returned junk" is a service-unavailability answer —
// never a 500, never a fake success.
var (
	ErrSuggestUnavailable = errors.New("chat: suggestion model unavailable")
	ErrNoSuggestions      = errors.New("chat: no usable suggestions returned")
)

// Input/output bounds for the suggest feature (DoS guards).
const (
	suggestHistoryLimit    = 10   // newest messages fed to the task model
	suggestMessageRunes    = 400  // per-message truncation
	suggestTranscriptRunes = 8000 // whole-transcript cap (~2k estimated tokens)
	suggestMinItems        = 2    // fewer usable lines than this → ErrNoSuggestions
	suggestMaxItems        = 5    // never return more than this many
	suggestItemMaxRunes    = 200  // per-suggestion cap
	suggestScanLines       = 40   // never parse more model output than this
)

// suggestInstruction asks for exactly 3 questions (keep in sync with the
// 3-5 accepted range: suggestMinItems..suggestMaxItems).
const suggestInstruction = "You predict what the user will ask next. Read the conversation " +
	"and write exactly 3 short questions the user might plausibly ask next. " +
	"Put each question on its own line and output nothing else: no numbering, " +
	"no bullets, no markdown, no quotes, no commentary. " +
	"Write the questions in the same language as the conversation."

// SuggestNext asks the instance task model for 3 short questions the user
// might ask next, based on the newest messages of the chat's active branch.
// userID must own the chat: missing/soft-deleted/foreign chats all answer
// ErrChatNotFound (same no-existence-leak rule as Send). Everything else is
// typed (ErrSuggestUnavailable / ErrNoSuggestions) so the caller can answer
// a clean status instead of dumping internals.
func (s *Service) SuggestNext(ctx context.Context, chatID, userID string) ([]string, error) {
	chat, err := s.loadChat(ctx, userID, chatID)
	if err != nil {
		return nil, err
	}
	// Same read path as prompt assembly: the active branch, newest rows only.
	msgs, err := s.Store.Messages.CurrentPath(ctx, chatID, chat.LeafID, suggestHistoryLimit)
	if err != nil {
		return nil, err
	}
	transcript := renderTranscript(msgs)
	if transcript == "" {
		return nil, ErrNoSuggestions // empty (or only-failed-turns) chat: nothing to build on
	}

	// Task-model dispatch goes through the same machinery — and the same
	// admin kill switches (provider disabled / key unreadable / model row
	// disabled) — that titles and summaries use.
	providerID, modelID, ok := s.taskModel(ctx)
	if !ok {
		slog.Warn("chat: suggest next: no task model configured")
		return nil, ErrSuggestUnavailable
	}
	prov, err := s.taskProvider(ctx, providerID, modelID)
	if err != nil {
		slog.Warn("chat: suggest next: task provider refused", "provider_id", providerID, "error", err)
		return nil, ErrSuggestUnavailable
	}

	// Bounded like every other task-model call, but attached to the request
	// context (a user is waiting, so their disconnect should cancel it).
	ctx, cancel := context.WithTimeout(ctx, backgroundTaskTimeout)
	defer cancel()

	out, err := s.Prov.Complete(ctx, prov, modelID, []providers.ChatMessage{
		{Role: "system", Content: suggestInstruction},
		{Role: "user", Content: "Conversation:\n" + transcript},
	})
	if err != nil {
		slog.Warn("chat: suggest next: task model call failed", "error", err)
		return nil, ErrSuggestUnavailable
	}
	items := parseSuggestions(out)
	if len(items) < suggestMinItems {
		slog.Warn("chat: suggest next: model output unusable", "items", len(items))
		return nil, ErrNoSuggestions
	}
	return items, nil
}

// renderTranscript formats the newest messages into "User: …"/"Assistant: …"
// lines (chronological). Each message is truncated to suggestMessageRunes
// and the whole transcript is capped at suggestTranscriptRunes (newest
// first, so the freshest turns always survive), keeping the task-model call
// cheap regardless of how long the chat has grown.
func renderTranscript(msgs []*store.Message) string {
	var kept []string
	used := 0
	for i := len(msgs) - 1; i >= 0 && len(kept) < suggestHistoryLimit; i-- {
		m := msgs[i]
		if m.Role != "user" && m.Role != "assistant" {
			continue // system rows are not conversation turns
		}
		if m.Role == "assistant" && m.Error != nil {
			continue // failed/partial turn: nothing worth building on
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if r := []rune(content); len(r) > suggestMessageRunes {
			content = string(r[:suggestMessageRunes])
		}
		line := "User: " + content
		if m.Role == "assistant" {
			line = "Assistant: " + content
		}
		if used+len(line) > suggestTranscriptRunes {
			break // newest window is full; older rows are out of budget
		}
		used += len(line)
		kept = append(kept, line)
	}
	slices.Reverse(kept)
	return strings.Join(kept, "\n")
}

// suggestNumPrefix matches list numbering the model was told not to emit
// ("1. ", "2) ", "3、", "(2):" …).
var suggestNumPrefix = regexp.MustCompile(`^\(*\d{1,2}[.．)）、:：]\s*`)

// suggestEmojiTrim is the whitespace/emoji-joiner cutset: plain spaces plus
// the variation selector (U+FE0F) and zero-width joiner (U+200D) that emoji
// decorations carry.
const suggestEmojiTrim = " \t\uFE0F\u200D"

// parseSuggestions turns raw model output into clean questions: split lines
// (bounded scan), drop code fences / lead-in chatter / empties, strip list
// markers, numbering, emoji bullets and quotes, cap length, dedupe
// case-insensitively and keep at most suggestMaxItems.
func parseSuggestions(raw string) []string {
	lines := strings.Split(raw, "\n")
	if len(lines) > suggestScanLines {
		lines = lines[:suggestScanLines]
	}
	out := make([]string, 0, suggestMaxItems)
	seen := make(map[string]bool, suggestMaxItems)
	for _, ln := range lines {
		s := cleanSuggestion(ln)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
		if len(out) == suggestMaxItems {
			break
		}
	}
	return out
}

func cleanSuggestion(line string) string {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "```") {
		return "" // blank line or code-fence artifact
	}
	s = strings.TrimLeft(s, "-–—*•·‣◦⁃+>» \t")
	s = suggestNumPrefix.ReplaceAllString(s, "")
	s = trimLeadingEmoji(s)
	s = strings.Trim(s, "-–—*•·‣◦⁃+>» \t\"'`“”‘’«»「」『』")
	if s == "" {
		return ""
	}
	// "Here are 3 ideas:" style lead-ins end in a colon; no question does.
	if r := []rune(s); r[len(r)-1] == ':' || r[len(r)-1] == '：' {
		return ""
	}
	// A usable line must contain at least one letter or digit.
	var alnum bool
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			alnum = true
			break
		}
	}
	if !alnum {
		return ""
	}
	if r := []rune(s); len(r) > suggestItemMaxRunes {
		s = string(r[:suggestItemMaxRunes])
	}
	return s
}

// trimLeadingEmoji strips emoji/symbol decorations a model bolts onto list
// items ("✨ ", "💡 ", incl. variation selectors and ZWJ joins) plus the
// whitespace around them.
func trimLeadingEmoji(s string) string {
	for {
		t := strings.TrimLeft(s, suggestEmojiTrim)
		for len(t) > 0 {
			r, size := utf8.DecodeRuneInString(t)
			if !unicode.Is(unicode.So, r) {
				break
			}
			t = t[size:]
		}
		if t == s {
			return t
		}
		s = t
	}
}
