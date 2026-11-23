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
	"sync"
	"sync/atomic"
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

// DoS guards for in-flight turns (contract §Chat service). Vars so tests can
// shrink them; production keeps the values below.
var (
	// maxConcurrentPerUser caps simultaneous streaming turns one user may
	// have in flight; over-limit sends are rejected with
	// errMsgTooManyConcurrent.
	maxConcurrentPerUser = 3
	// maxConcurrentGlobal caps simultaneous turns across the whole process
	// (each turn holds a DB writer, an upstream conn and stream buffers).
	maxConcurrentGlobal = 32
)

// Client-visible terminal error strings (SSE `error` payloads).
const (
	// errMsgTooManyConcurrent is surfaced to the client as an SSE `error`
	// event (429-style: the stream starts and terminates immediately).
	errMsgTooManyConcurrent = "too many concurrent requests"
	// errMsgProviderDisabled surfaces when the chat's provider row has been
	// disabled by an admin after the chat was created.
	errMsgProviderDisabled = "model provider disabled"
	errMsgKeyUnreadable    = "the stored provider key can't be decrypted — the server secret changed after it was saved; re-enter the API key in Admin → Providers"
	// errMsgModelDisabled surfaces when the chat's cached model row has been
	// disabled by an admin after the chat was created (SECREV-3 S2-1: the
	// per-model kill must stop spend even into pre-existing chats).
	errMsgModelDisabled = "chat: this model has been disabled by an admin"
	// errMsgModelGone surfaces when the chat's model is no longer cached for
	// its provider at all (removed upstream by a later sync). Same verdict
	// as create/switch: validateChatModel (internal/apichat/chats.go) answers
	// an uncached model 404 "model not found", so the send path refuses too
	// — a vanished model is treated like a disabled one, never passed
	// through to upstream on trust.
	errMsgModelGone = "chat: this model is no longer available"

	// turnUpstreamTimeout bounds the whole upstream phase (connect + full
	// stream) of one turn so a stalled provider cannot pin a turn slot,
	// goroutine and connection forever on top of the client-side watchdogs
	// in internal/providers.
	turnUpstreamTimeout = 10 * time.Minute
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
	// persisted plus the chat it belongs to. UserMessageID names the user
	// row persisted for this turn (normal send + branch edit); "" when the
	// turn re-uses an existing user row (regenerate).
	MetaData struct {
		MessageID     string `json:"message_id"`
		ChatID        string `json:"chat_id"`
		UserMessageID string `json:"user_message_id,omitempty"`
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
	// SourcesData is the `sources` payload. Error is set when the turn's
	// web search FAILED (items then carries only non-search enrichments);
	// it is streamed for the live turn only — never persisted — so the
	// composer chip and a reload behave like Open WebUI's status line.
	SourcesData struct {
		Items []SourceItem `json:"items"`
		Error string       `json:"error,omitempty"`
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

	// Client context: the browser's IANA timezone and (opt-in) approximate
	// location, folded into the system prompt's user-context line so
	// "today / here / nearby" resolve without the model guessing.
	// Timezone is validated via time.LoadLocation before use; HasLoc
	// distinguishes "shared 0,0" from "not shared".
	Timezone string
	LocLat   float64
	LocLon   float64
	HasLoc   bool
	// Branch makes From an insert-beside instead of a truncate-and-redo: the
	// anchor and everything after it stay, the new version lands as its
	// sibling (Open WebUI's ‹1/2› version model). Only meaningful with From.
	Branch bool

	// branchParent is the parent the replacement message gets — the anchor's
	// own parent, resolved by Send. branchUser marks an edited user anchor
	// whose new content is persisted as the sibling user row. branchAnchor
	// keeps the assistant anchor itself so Continue-response can replay its
	// partial text into the prompt (prompt-only, never persisted).
	branchParent string
	branchUser   bool
	branchAnchor *store.Message
}

// Enricher is the pluggable chat-grounding hook (FEATURES-V2 §BE-RAG
// coordination law). Feature packages (internal/kb) register one at wiring
// time via RegisterEnricher; run() calls each AFTER web search and BEFORE
// the upstream call, merging returned SourceItems into the single `sources`
// SSE event and extra into the SearchCtx grounding text (a trailing USER
// turn — untrusted content never gets a system role, see
// providers.buildMessages). Returning (nil, "", nil) means "nothing this
// turn". An enricher error degrades gracefully: it is logged and the turn
// proceeds without that enrichment.
type Enricher func(ctx context.Context, chat *store.Chat, userMsg providers.ChatMessage) (sources []SourceItem, extra string, err error)

type enricherEntry struct {
	name string
	fn   Enricher
}

// Service drives chat turns. Dependencies are interfaces/pointers wired in main.
type Service struct {
	Cfg    *config.Config
	Store  *store.Store
	Prov   providers.Client
	Search *search.Service

	// In-flight turn accounting for the DoS caps above: activeTotal is an
	// atomic global tally, activeUsers is guarded by turnsMu.
	turnsMu     sync.Mutex
	activeUsers map[string]int
	activeTotal atomic.Int64

	// Enricher registry (see Enricher): registered at wiring time, read per
	// turn via enricherSnapshot. enrichMu guards registration.
	enrichMu  sync.Mutex
	enrichers []enricherEntry
}

// New builds the chat service.
func New(cfg *config.Config, st *store.Store, prov providers.Client, srch *search.Service) *Service {
	return &Service{Cfg: cfg, Store: st, Prov: prov, Search: srch, activeUsers: map[string]int{}}
}

// RegisterEnricher appends a grounding enricher (wiring-time; called by
// feature packages such as internal/kb). Registration order is execution
// order.
func (s *Service) RegisterEnricher(name string, fn Enricher) {
	if fn == nil {
		return
	}
	s.enrichMu.Lock()
	defer s.enrichMu.Unlock()
	s.enrichers = append(s.enrichers, enricherEntry{name: name, fn: fn})
}

// enricherSnapshot copies the registry so the request path never holds the
// registration lock.
func (s *Service) enricherSnapshot() []enricherEntry {
	s.enrichMu.Lock()
	defer s.enrichMu.Unlock()
	if len(s.enrichers) == 0 {
		return nil
	}
	out := make([]enricherEntry, len(s.enrichers))
	copy(out, s.enrichers)
	return out
}

// acquireTurn reserves one in-flight turn slot for userID under the per-user
// and global caps. On rejection nothing is held; on success the caller must
// call the returned release func exactly once when the turn ends.
func (s *Service) acquireTurn(userID string) (release func(), ok bool) {
	// The global tally doubles as a transient reservation: a request that
	// loses either race un-does its increment before any turn starts, so
	// neither cap can be over-admitted — a brief over-count only ever causes
	// an extra rejection, never more concurrency than allowed.
	if s.activeTotal.Add(1) > int64(maxConcurrentGlobal) {
		s.activeTotal.Add(-1)
		return nil, false
	}
	s.turnsMu.Lock()
	if s.activeUsers == nil { // tolerate a hand-built Service (tests/fakes)
		s.activeUsers = map[string]int{}
	}
	if s.activeUsers[userID] >= maxConcurrentPerUser {
		s.turnsMu.Unlock()
		s.activeTotal.Add(-1)
		return nil, false
	}
	s.activeUsers[userID]++
	s.turnsMu.Unlock()

	var once sync.Once
	release = func() {
		once.Do(func() {
			s.turnsMu.Lock()
			if n := s.activeUsers[userID] - 1; n > 0 {
				s.activeUsers[userID] = n
			} else {
				delete(s.activeUsers, userID)
			}
			s.turnsMu.Unlock()
			s.activeTotal.Add(-1)
		})
	}
	return release, true
}

// Send persists the user message, streams the assistant reply and returns a
// channel of Events terminated by done|error. The channel is closed after the
// terminal event. BE-chat implements compaction/title/summarize per contract.
//
// Pre-stream problems that must map to an HTTP status (chat not visible to
// this user, bad regenerate anchor) are returned synchronously so the caller
// can answer 404 before committing to an SSE stream; everything after that
// arrives as an `error` Event — including the concurrency-cap rejection
// below, which is surfaced as an SSE error event (429-style) carrying
// errMsgTooManyConcurrent and terminating the stream immediately.
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

	// DoS cap — checked BEFORE any mutation (a rejected regenerate must not
	// delete the tail).
	release, ok := s.acquireTurn(userID)
	if !ok {
		out := make(chan Event, 1)
		out <- Event{Type: EventError, Data: ErrorData{Message: errMsgTooManyConcurrent}}
		close(out)
		return out, nil
	}

	// Regenerate anchor resolution. Legacy flow (Branch=false): the tail is
	// dropped BEFORE building the prompt; no new user row. Branch flow: the
	// anchor stays in place — the replacement lands as its sibling under the
	// same parent, so older versions and their continuations survive.
	if opts.From != "" {
		anchor, err := s.Store.Messages.Get(ctx, chatID, opts.From)
		if err != nil {
			release()
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrMessageNotFound
			}
			return nil, err
		}
		opts.branchParent = anchor.ParentID
		opts.branchUser = opts.Branch && anchor.Role == "user"
		if opts.Branch && anchor.Role == "assistant" {
			opts.branchAnchor = anchor // continue-response replays it in-prompt
		}
		if !opts.Branch {
			if err := s.Store.Messages.DeleteFrom(ctx, chatID, opts.From); err != nil {
				release()
				if errors.Is(err, store.ErrNotFound) {
					return nil, ErrMessageNotFound
				}
				return nil, err
			}
			// The deleted tail may have held the leaf — pull it back to the
			// anchor's parent so the next turn continues from what survived.
			if err := s.Store.Chats.SetLeaf(ctx, chatID, opts.branchParent); err != nil {
				slog.Warn("chat: rewind leaf failed", "chat_id", chatID, "error", err)
			}
		}
	}

	out := make(chan Event, 64)
	go s.run(ctx, out, chat, userMsg, opts, release)
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

// checkChatModel enforces the admin models cache for the chat's stored model
// on the SEND path (SECREV-3 S2-1). A non-empty refuse is the client-safe
// SSE error message (errMsgModelDisabled / errMsgModelGone); err is non-nil
// only when the cache itself is unreadable — the caller must then fail the
// turn generically, never allow it. See run() step 7b for the policy's
// create-path provenance.
func (s *Service) checkChatModel(ctx context.Context, chat *store.Chat) (refuse string, err error) {
	if chat.ModelID == "" {
		return "", nil // provider-only chat: create accepts it, so send does too
	}
	models, err := s.Store.Models.ForProvider(ctx, chat.ProviderID)
	if err != nil {
		return "", err
	}
	for i := range models {
		if models[i].ModelID != chat.ModelID {
			continue
		}
		if !models[i].Enabled {
			return errMsgModelDisabled, nil
		}
		return "", nil
	}
	return errMsgModelGone, nil
}

// run is the streaming goroutine: persist → meta → title/search → upstream →
// persist assistant → done|error. release returns the turn slot acquired by
// Send and must be non-nil.
func (s *Service) run(ctx context.Context, out chan<- Event, chat *store.Chat, userMsg providers.ChatMessage, opts SendOpts, release func()) {
	defer close(out)
	defer release() // runs BEFORE close(out) (LIFO): a waiting next send sees the slot free
	chatID := chat.ID
	assistantID := uuid.NewString()

	// 1. Persist the user message (normal turns, and branch edits which land
	// as a sibling of the edited user row). pathLeaf is the branch tip the
	// prompt is built from.
	firstExchange := false
	var userMsgID string
	persistUser := opts.From == "" || opts.branchUser
	var pathLeaf string
	if persistUser {
		parent := chat.LeafID
		if opts.From != "" {
			parent = opts.branchParent // edit: sibling slot beside the anchor
		}
		before, err := s.Store.Messages.Count(ctx, chatID)
		if err != nil {
			s.fail(ctx, out, "chat: load failed", err)
			return
		}
		firstExchange = before == 0
		m := &store.Message{ChatID: chatID, Role: "user", ParentID: parent, Content: userMsg.Content}
		if err := s.Store.Messages.Append(ctx, m); err != nil {
			s.fail(ctx, out, "chat: persist user message failed", err)
			return
		}
		userMsgID = m.ID
		pathLeaf = m.ID
		if err := s.Store.Chats.SetLeaf(ctx, chatID, m.ID); err != nil {
			slog.Warn("chat: set leaf failed", "chat_id", chatID, "error", err)
		}
	} else if opts.Branch {
		pathLeaf = opts.branchParent // regenerate: path stops beside the anchor
	}
	// legacy truncate flow keeps pathLeaf == "": history() falls back to the
	// linear List, which is exactly what the rowid-based tail delete implies.

	// 2. meta carries the (not yet persisted) assistant message id.
	if !emit(ctx, out, EventMeta, MetaData{MessageID: assistantID, ChatID: chatID, UserMessageID: userMsgID}) {
		return
	}

	// 3. Kick off title generation early so it overlaps compaction/search.
	titleCh, titleStart := s.startTitle(ctx, chat, firstExchange, userMsg)

	// 4. History (summary + branch path), system prompt, compaction.
	total, history, summary, err := s.history(ctx, chatID, pathLeaf)
	if err != nil {
		s.fail(ctx, out, "chat: load history failed", err)
		return
	}
	if opts.From != "" && !opts.Branch && (len(history) == 0 || history[len(history)-1].Role != "user") {
		// Legacy regenerate anchored on the user message: re-add its content
		// to the prompt only (never persisted). Branch edits persist the
		// sibling row, so that path already carries it.
		history = append(history, &store.Message{Role: "user", Content: userMsg.Content})
	}
	promptHist := history
	if opts.branchAnchor != nil {
		// Continue-response (OWUI): replay the anchor's partial answer and
		// follow it with the directive as prompt-only turns — the sibling
		// replacement continues from exactly where the text stopped.
		promptHist = append(append([]*store.Message{}, history...),
			&store.Message{Role: "assistant", Content: opts.branchAnchor.Content},
			&store.Message{Role: "user", Content: userMsg.Content})
	}
	system := s.systemPrompt(ctx, opts)
	contextLen := s.modelContextLen(ctx, chat)
	msgs, compacted := assemblePrompt(system, summary, promptHist, contextLen, userMsgID, opts.Images)

	// Summarize fires async; it must never block the stream. maybeSummarize
	// detaches its worker context itself (see fireCtx).
	//nolint:contextcheck // fire-and-forget by design (contract §Chat service)
	s.maybeSummarize(ctx, chatID, total, history, summary, compacted)

	// 5. Web search + enrichers → ONE `sources` event + SearchCtx for the
	// upstream call. Enrichment never aborts the turn: failures degrade to
	// "no extra context" (logged), matching the web-search degradation.
	var sourcesJSON, searchCtx string
	var items []SourceItem
	ranSearch := false
	searchErr := ""
	if opts.Search && s.Search != nil {
		results, ran, err := s.searchWeb(ctx, userMsg.Content)
		ranSearch = ran
		if ran {
			if err != nil {
				// Degrade gracefully: empty sources, no SearchCtx, keep streaming.
				// The message is surfaced on the `sources` event so the UI can
				// show WHY (a silently missing search context reads to users as
				// "the model refuses to search").
				searchErr = err.Error()
				slog.Warn("chat: web search failed", "chat_id", chatID, "error", err)
				results = nil
			}
			for _, r := range results {
				items = append(items, SourceItem{Title: r.Title, URL: r.URL})
			}
			if len(results) > 0 {
				searchCtx = search.ComposeContext(results)
			}
		}
	}
	for _, en := range s.enricherSnapshot() {
		extraSources, extra, err := en.fn(ctx, chat, userMsg)
		if err != nil {
			slog.Warn("chat: enricher failed", "chat_id", chatID, "enricher", en.name, "error", err)
			continue
		}
		items = append(items, extraSources...)
		if extra != "" {
			if searchCtx != "" {
				searchCtx += "\n\n"
			}
			searchCtx += extra
		}
	}
	if len(items) > 0 {
		if b, err := json.Marshal(items); err == nil {
			sourcesJSON = string(b)
		}
	}
	if ranSearch || len(items) > 0 {
		if !emit(ctx, out, EventSources, SourcesData{Items: items, Error: searchErr}) {
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
					tctx, cancel := fireCtx(ctx)
					defer cancel()
					s.applyTitle(tctx, chatID, res)
				}
			}()
		}
	}

	// 7. Upstream call. The provider row's enable flag is authoritative here
	// (admins disable providers to stop all traffic immediately, even into
	// pre-existing chats).
	provRow, err := s.Store.Providers.Get(ctx, chat.ProviderID)
	if err != nil {
		s.fail(ctx, out, "chat: provider not configured for this chat", err)
		return
	}
	if !provRow.Enabled {
		slog.Warn("chat: provider disabled; refusing turn", "chat_id", chatID, "provider_id", chat.ProviderID)
		emit(ctx, out, EventError, ErrorData{Message: errMsgProviderDisabled})
		return
	}
	// The stored API key failed to decrypt (SESSION_SECRET rotated or the
	// blob corrupted since it was saved). The row still reads fine so admin
	// can repair it, but sending upstream with an empty key would surface as
	// a misleading provider-side 401 — say what is actually wrong.
	if provRow.KeyUnreadable {
		slog.Warn("chat: provider key unreadable; refusing turn", "chat_id", chatID, "provider_id", chat.ProviderID)
		emit(ctx, out, EventError, ErrorData{Message: errMsgKeyUnreadable})
		return
	}

	// 7b. The chat's cached model row is authoritative the same way
	// (SECREV-3 S2-1): a per-model disable must stop spend even into
	// pre-existing chats, with the same immediacy as the provider kill above.
	// Policy mirrors validateChatModel on the create/switch path
	// (internal/apichat/chats.go) exactly, so the two agree:
	//   - chat without a model (ModelID == ""): no check — create accepts
	//     provider-only chats, including providers whose catalog was never
	//     synced (zero cached rows);
	//   - cached row with enabled=0: refuse (the admin toggled it off);
	//   - no cached row for (provider, model), zero-row provider included:
	//     refuse — create answers this 404 "model not found", so a model
	//     that vanished from the cache (deleted upstream by a later sync)
	//     must not keep answering on pre-existing chats either.
	// One indexed read on the models cache, same place in the flow as the
	// provider row read above.
	if refuse, err := s.checkChatModel(ctx, chat); err != nil {
		s.fail(ctx, out, "chat: model lookup failed", err)
		return
	} else if refuse != "" {
		slog.Warn("chat: model disabled; refusing turn", "chat_id", chatID,
			"provider_id", chat.ProviderID, "model_id", chat.ModelID, "error", refuse)
		emit(ctx, out, EventError, ErrorData{Message: refuse})
		return
	}

	// Whole-turn deadline over the upstream phase (connect + stream): the
	// providers-client watchdogs catch silent connections, this caps slow
	// but not silent upstreams too.
	uctx, cancelUpstream := context.WithTimeout(ctx, turnUpstreamTimeout)
	defer cancelUpstream()
	chunks, err := s.Prov.StreamChat(uctx, providerFromRow(provRow), providers.StreamOpts{
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
		case <-uctx.Done():
			if ctx.Err() != nil {
				streamErr = "stream canceled"
			} else {
				streamErr = "stream timed out"
			}
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
	// run IS the worker goroutine here, so owning cancel via defer is safe:
	// it fires when run returns (right after the terminal event).
	pctx, cancelPersist := fireCtx(ctx)
	defer cancelPersist()
	amParent := userMsgID
	if amParent == "" {
		amParent = opts.branchParent // regenerate (both modes): sibling of the anchor
	}
	am := &store.Message{
		ID:          assistantID,
		ChatID:      chatID,
		Role:        "assistant",
		ParentID:    amParent,
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
	} else if err := s.Store.Chats.SetLeaf(pctx, chatID, assistantID); err != nil {
		slog.Warn("chat: advance leaf failed", "chat_id", chatID, "error", err)
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
// The CALLING worker goroutine owns the returned cancel and must defer it —
// deliberately no internal `<-ctx.Done(); cancel()` watcher goroutine: those
// each lived for the full 60s deadline per fire-and-forget task (goroutine
// retention under load).
func fireCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), backgroundTaskTimeout)
}

// settingSearchURL is the admin settings key the admin UI writes and
// apimgr/admin.go reports as search_enabled.
const settingSearchURL = "search_url"

// searchN is how many SearXNG hits a turn requests: SEARCH_MAX_RESULTS from
// config (default 5), never above the search service's HardCap.
func (s *Service) searchN() int {
	n := search.MaxResults
	if s.Cfg != nil && s.Cfg.SearchMaxResults > 0 {
		n = s.Cfg.SearchMaxResults
	}
	if n > search.HardCap {
		n = search.HardCap
	}
	return n
}

// searchWeb runs the turn's web search, resolving the SearXNG target in the
// order the rest of the app already implies: cfg SEARXNG_URL (service
// enabled) wins; when it is unset, the admin-configured "search_url" setting
// is dialed via the same guarded machinery (per-request base override on
// search.Service). ran=false means no target is configured at all, and the
// caller then emits no `sources` event.
func (s *Service) searchWeb(ctx context.Context, q string) (results []search.Result, ran bool, err error) {
	if s.Search.Enabled {
		results, err := s.Search.Search(ctx, q, s.searchN())
		return results, true, err
	}
	if s.Cfg != nil && strings.TrimSpace(s.Cfg.SearXNGURL) != "" {
		return nil, false, nil // env set but service disabled — nothing to dial
	}
	v, err := s.Store.Settings.Get(ctx, settingSearchURL)
	if err != nil {
		slog.Warn("chat: read search_url setting failed", "error", err)
		return nil, false, nil
	}
	target := ""
	if v != nil {
		target = strings.TrimSpace(*v)
	}
	if target == "" {
		return nil, false, nil
	}
	results, err = s.Search.SearchURL(ctx, target, q, s.searchN())
	return results, true, err
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
