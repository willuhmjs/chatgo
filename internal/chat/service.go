// Package chat orchestrates a chat turn: history compaction, persistence,
// upstream streaming, titles and summaries. BE-chat owns the logic;
// these types and the constructor shape are frozen by docs/CONTRACTS.md.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"chatgo/internal/config"
	"chatgo/internal/providers"
	"chatgo/internal/search"
	"chatgo/internal/store"

	"github.com/google/uuid"
)

// Sentinels for pre-stream failures. A chat the caller cannot see
// (missing, soft-deleted, or owned by someone else) always maps to
// ErrChatNotFound — this layer never leaks existence (contract §Non-negotiables).
var (
	ErrChatNotFound    = errors.New("chat: chat not found")
	ErrMessageNotFound = errors.New("chat: message not found")
)

// Event is an api-side SSE event (see contract §SSE protocol). Type is one of
// meta|title|sources|token|done|error; Data is the JSON payload for that type.
type Event struct {
	Type string
	Data any
}

// Event type names (contract §SSE protocol).
const (
	EventMeta    = "meta"
	EventTitle   = "title"
	EventSources = "sources"
	EventToken   = "token"
	EventDone    = "done"
	EventError   = "error"
)

// Event payloads, JSON shapes frozen by contract §SSE protocol.
type (
	// MetaData is the `meta` payload: the assistant message id that will be
	// persisted plus the chat it belongs to.
	MetaData struct {
		MessageID string `json:"message_id"`
		ChatID    string `json:"chat_id"`
	}
	// TitleData is the `title` payload.
	TitleData struct {
		Title string `json:"title"`
	}
	// SourceItem is one search hit inside the `sources` payload.
	SourceItem struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	// SourcesData is the `sources` payload.
	SourcesData struct {
		Items []SourceItem `json:"items"`
	}
	// TokenData is one `token` payload.
	TokenData struct {
		Content string `json:"content"`
	}
	// UsageData is the usage object inside `done`.
	UsageData struct {
		In  int `json:"in"`
		Out int `json:"out"`
	}
	// DoneData is the `done` payload; Usage is nil when upstream sent none.
	DoneData struct {
		Usage *UsageData `json:"usage"`
	}
	// ErrorData is the `error` payload.
	ErrorData struct {
		Message string `json:"message"`
	}
)

// SendOpts are per-turn options from POST /api/chats/{id}/messages.
type SendOpts struct {
	Search bool     // enable SearXNG web search
	Images []string // data: URLs attached to the user message
	From   string   // message id to regenerate from ("" = normal append)
}

// Service drives chat turns. Dependencies are interfaces/pointers wired in main.
type Service struct {
	Cfg    *config.Config
	Store  *store.Store
	Prov   providers.Client
	Search *search.Service
}

// New builds the chat service.
func New(cfg *config.Config, st *store.Store, prov providers.Client, srch *search.Service) *Service {
	return &Service{Cfg: cfg, Store: st, Prov: prov, Search: srch}
}

// Send persists the user message, streams the assistant reply and returns a
// channel of Events terminated by done|error. The channel is closed after the
// terminal event. BE-chat implements compaction/title/summarize per contract.
//
// Pre-stream problems that must map to an HTTP status (chat not visible to
// this user, bad regenerate anchor) are returned synchronously so the caller
// can answer 404 before committing to an SSE stream; everything after that
// arrives as an `error` Event.
//
// CONTRACT-DEVIATION: SendOpts.Images carries data: URLs, but the
// message_images table references file ids and no store repo method exists
// for it, so images are sent to the upstream this turn but not persisted
// (BE-apichat owns the file-id ↔ message linkage).
func (s *Service) Send(ctx context.Context, userID, chatID string, userMsg providers.ChatMessage, opts SendOpts) (<-chan Event, error) {
	chat, err := s.loadChat(ctx, userID, chatID)
	if err != nil {
		return nil, err
	}

	// Regenerate: drop the tail BEFORE building the prompt; no new user row.
	if opts.From != "" {
		if err := s.Store.Messages.DeleteFrom(ctx, chatID, opts.From); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrMessageNotFound
			}
			return nil, err
		}
	}

	out := make(chan Event, 64)
	go s.run(ctx, out, chat, userMsg, opts)
	return out, nil
}

// loadChat fetches a chat and enforces ownership. Non-existent, deleted and
// foreign chats are indistinguishable (all → ErrChatNotFound).
func (s *Service) loadChat(ctx context.Context, userID, chatID string) (*store.Chat, error) {
	chat, err := s.Store.Chats.Get(ctx, chatID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrChatNotFound
		}
		return nil, err
	}
	if chat.UserID != userID {
		return nil, ErrChatNotFound
	}
	return chat, nil
}

// run is the streaming goroutine: persist → meta → title/search → upstream →
// persist assistant → done|error.
func (s *Service) run(ctx context.Context, out chan<- Event, chat *store.Chat, userMsg providers.ChatMessage, opts SendOpts) {
	defer close(out)
	chatID := chat.ID
	assistantID := uuid.NewString()

	// 1. Persist the user message first (normal turns only).
	firstExchange := false
	var userMsgID string
	if opts.From == "" {
		before, err := s.Store.Messages.Count(ctx, chatID)
		if err != nil {
			s.fail(ctx, out, "chat: load failed", err)
			return
		}
		firstExchange = before == 0
		m := &store.Message{ChatID: chatID, Role: "user", Content: userMsg.Content}
		if err := s.Store.Messages.Append(ctx, m); err != nil {
			s.fail(ctx, out, "chat: persist user message failed", err)
			return
		}
		userMsgID = m.ID
	}

	// 2. meta carries the (not yet persisted) assistant message id.
	if !emit(ctx, out, EventMeta, MetaData{MessageID: assistantID, ChatID: chatID}) {
		return
	}

	// 3. Kick off title generation early so it overlaps compaction/search.
	titleCh, titleStart := s.startTitle(ctx, chat, firstExchange, userMsg)

	// 4. History (summary + tail), system prompt, compaction.
	total, history, summary, err := s.history(ctx, chatID)
	if err != nil {
		s.fail(ctx, out, "chat: load history failed", err)
		return
	}
	if opts.From != "" && (len(history) == 0 || history[len(history)-1].Role != "user") {
		// Regenerate anchor was itself the user message: re-add its content
		// to the prompt only (never persisted).
		history = append(history, &store.Message{Role: "user", Content: userMsg.Content})
	}
	system := s.systemPrompt(ctx)
	contextLen := s.modelContextLen(ctx, chat)
	msgs, compacted := assemblePrompt(system, summary, history, contextLen, userMsgID, opts.Images)

	// Summarize fires async; it must never block the stream.
	//nolint:contextcheck // fire-and-forget by design (contract §Chat service)
	s.maybeSummarize(fireCtx(ctx), chatID, total, history, summary, compacted)

	// 5. Web search → `sources` event + SearchCtx for the upstream call.
	var sourcesJSON, searchCtx string
	if opts.Search && s.Search != nil && s.Search.Enabled {
		results, err := s.Search.Search(ctx, userMsg.Content, searchMaxResults)
		if err != nil {
			// Degrade gracefully: empty sources, no SearchCtx, keep streaming.
			slog.Warn("chat: web search failed", "chat_id", chatID, "error", err)
			results = nil
		}
		items := make([]SourceItem, 0, len(results))
		for _, r := range results {
			items = append(items, SourceItem{Title: r.Title, URL: r.URL})
		}
		if len(items) > 0 {
			if b, err := json.Marshal(items); err == nil {
				sourcesJSON = string(b)
			}
			searchCtx = search.ComposeContext(results)
		}
		if !emit(ctx, out, EventSources, SourcesData{Items: items}) {
			return
		}
	}

	// 6. Title event (if ready within the budget; see title.go).
	if titleCh != nil {
		if title, ok := waitTitle(titleCh, titleStart); ok {
			if s.applyTitle(ctx, chatID, title) {
				if !emit(ctx, out, EventTitle, TitleData{Title: title}) {
					return
				}
			}
		} else {
			// Too late to emit, but persist it anyway so the chat gets a
			// proper title eventually and never re-titles on later turns.
			//nolint:contextcheck // fire-and-forget by design
			go func() {
				if res, ok := <-titleCh; ok && res != "" {
					s.applyTitle(fireCtx(ctx), chatID, res)
				}
			}()
		}
	}

	// 7. Upstream call.
	provRow, err := s.Store.Providers.Get(ctx, chat.ProviderID)
	if err != nil {
		s.fail(ctx, out, "chat: provider not configured for this chat", err)
		return
	}
	chunks, err := s.Prov.StreamChat(ctx, providerFromRow(provRow), providers.StreamOpts{
		ProviderID: chat.ProviderID,
		ModelID:    chat.ModelID,
		Messages:   msgs,
		Params:     chatParams(chat.ParamsJSON),
		SearchCtx:  searchCtx,
	})
	if err != nil {
		s.fail(ctx, out, "chat: upstream request failed", err)
		return
	}

	// 8. Stream chunks → token events.
	var (
		content   strings.Builder
		usage     *providers.Usage
		finished  bool
		streamErr string
	)
streamLoop:
	for {
		select {
		case <-ctx.Done():
			streamErr = "stream canceled"
			break streamLoop
		case c, ok := <-chunks:
			if !ok {
				break streamLoop
			}
			if c.Content != "" {
				content.WriteString(c.Content)
				if !emit(ctx, out, EventToken, TokenData{Content: c.Content}) {
					streamErr = "stream canceled"
					break streamLoop
				}
			}
			if c.Usage != nil {
				u := *c.Usage
				usage = &u
			}
			if c.FinishReason != "" {
				finished = true
			}
		}
	}
	if streamErr == "" && !finished {
		// Channel closed without a finish reason: upstream died mid-stream.
		streamErr = "upstream stream ended unexpectedly"
	}

	// 9. Persist the assistant message even if the client went away.
	pctx := fireCtx(ctx)
	am := &store.Message{
		ID:          assistantID,
		ChatID:      chatID,
		Role:        "assistant",
		Content:     content.String(),
		SourcesJSON: sourcesJSON,
	}
	if usage != nil {
		am.TokensIn, am.TokensOut = usage.In, usage.Out
	}
	if streamErr != "" {
		e := streamErr
		am.Error = &e
	}
	if err := s.Store.Messages.Append(pctx, am); err != nil {
		slog.Error("chat: persist assistant message failed", "chat_id", chatID, "error", err)
	}
	if err := s.Store.Chats.TouchUpdatedAt(pctx, chatID, time.Now().Unix()); err != nil {
		slog.Warn("chat: touch chat failed", "chat_id", chatID, "error", err)
	}

	// 10. Terminal event.
	if streamErr != "" {
		emit(ctx, out, EventError, ErrorData{Message: streamErr})
		return
	}
	var ud *UsageData
	if usage != nil {
		ud = &UsageData{In: usage.In, Out: usage.Out}
	}
	emit(ctx, out, EventDone, DoneData{Usage: ud})
}

// fail emits an error Event; err is logged but kept out of the client
// payload for internal (store) failures.
func (s *Service) fail(ctx context.Context, out chan<- Event, msg string, err error) {
	slog.Error(msg, "error", err)
	emit(ctx, out, EventError, ErrorData{Message: msg})
}

// emit sends ev unless the consumer went away (ctx canceled).
func emit(ctx context.Context, out chan<- Event, typ string, data any) bool {
	select {
	case out <- Event{Type: typ, Data: data}:
		return true
	case <-ctx.Done():
		return false
	}
}

// fireCtx detaches from request cancellation (client disconnects must not
// abort persistence/summaries) and bounds the work with its own deadline.
func fireCtx(ctx context.Context) context.Context {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundTaskTimeout)
	// cancel runs when the derived work returns or the deadline hits.
	go func() {
		<-pctx.Done()
		cancel()
	}()
	return pctx
}

// chatParams decodes chats.params_json into upstream passthrough params.
func chatParams(paramsJSON string) map[string]any {
	if paramsJSON == "" || paramsJSON == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &m); err != nil {
		return nil
	}
	return m
}

func providerFromRow(p *store.Provider) providers.Provider {
	return providers.Provider{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, APIKey: p.APIKey}
}
