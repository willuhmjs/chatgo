// Package kb implements the FEATURES-V2 §BE-RAG knowledge-base feature:
// per-user collections, multipart text ingest (chunk → provider embeddings →
// float32 little-endian BLOB), brute-scan cosine search, and chat-turn
// enrichment registered into internal/chat's Enricher registry.
//
// Coordination law (FEATURES-V2): this package owns its DDL (executed by
// Init against store.DB — store/migrate.go is never touched) and exposes
// Register(mux, *Deps) for the integrator. Private per user, like chats:
// a collection the caller does not own answers 404, never 403.
//
// RAM discipline: zero startup goroutines, zero caches. All work happens on
// request paths. Ingest streams through io.LimitReader (5 MiB/file); a chat
// enrichment or search runs a TWO-PASS scan — pass 1 streams only the
// embedding BLOBs into a top-k window, pass 2 loads content text for the
// k survivors. Embeddings go upstream in batches of ≤64 texts.
package kb

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/auth"
	"chatgo/internal/chat"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

// Ingest limits (FEATURES-V2 §RAM discipline).
const (
	// MaxFileBytes is the hard per-file ingest cap.
	MaxFileBytes int64 = 5 << 20
	// maxFilesPerRequest bounds one multipart POST.
	maxFilesPerRequest = 8
	// multipartSlack covers boundary framing on top of file bytes.
	multipartSlack int64 = 2 << 20

	// Chunker: ~800 words with ~80 words of overlap (FEATURES-V2 spec).
	chunkWords   = 800
	chunkOverlap = 80
	// maxChunkBytes hard-caps one chunk even when "words" are huge
	// (a single multi-megabyte token cannot pin memory via one chunk).
	maxChunkBytes = 32 << 10

	// embedBatchMax caps texts per /embeddings request (FEATURES-V2 spec).
	embedBatchMax = 64
	// embedTimeout bounds one embeddings HTTP exchange.
	embedTimeout = 60 * time.Second
	// embedBodyMax caps the parsed embeddings response body.
	embedBodyMax = 16 << 20

	// searchDefaultK / searchMaxK bound POST /api/kb/{id}/search top_k.
	searchDefaultK = 6
	searchMaxK     = 25
	// enrichTopK is the fixed chunk budget injected into a chat turn.
	enrichTopK = 6
	// enrichTimeout bounds one enricher run (embed call + scan).
	enrichTimeout = 30 * time.Second

	// maxScanChunks caps how many chunk rows one scan reads (personal
	// scale; anything beyond is ignored, oldest files first). Rows stream
	// through the score window, so this cap bounds scan work — retained
	// RAM no longer scales with it (pass 1 keeps only the k candidates).
	maxScanChunks = 20000

	// maxScanBytes caps the cumulative embedding BLOB bytes one scan
	// touches, beside the row cap: a misbehaving embeddings provider that
	// stored pathologically wide vectors cannot make a single scan reach
	// unbounded data. Sized so the row cap binds FIRST at realistic
	// vector sizes — 20k rows × 1536-dim float32 (OpenAI small, the
	// widest common embedding) = 117.2 MiB < 128 MiB. Rows past either
	// cap are ignored silently — the SAME semantics the row cap always
	// had: top-k over a truncated scan is weaker, never wrong; no
	// partial-results flag because that would reshape the search API.
	maxScanBytes int64 = 128 << 20

	// maxCollectionsPerChat caps kb_collection_ids honored from a chat's
	// params_json (prompt-injection/RAM surface control).
	maxCollectionsPerChat = 8

	// settingEmbedModel is the optional admin setting "providerID:modelID"
	// that pins the embeddings model explicitly.
	settingEmbedModel = "kb_embed_model"
	// settingTaskModel mirrors internal/chat/prompt.go's key.
	settingTaskModel = "task_model"
)

// Sentinel errors mapped onto HTTP statuses by fail().
var (
	// ErrNotFound: unknown or foreign collection/file (no existence leak).
	ErrNotFound = errors.New("kb: not found")
	// ErrNoEmbedModel: no embeddings-capable provider is configured.
	ErrNoEmbedModel = errors.New(`kb: no embeddings model configured — sync a provider offering an embedding model (model id containing "embed", e.g. text-embedding-3-small) or set the kb_embed_model setting to "providerID:modelID"`)
	// ErrEmbedUpstream wraps every embeddings transport/status failure so
	// handlers can answer a single clean 502 without leaking upstream detail.
	ErrEmbedUpstream = errors.New("kb: embeddings provider failed")
)

// Deps is the kb wiring set (small by coordination law). Chat is optional:
// when set, Register also adds the KB enricher to the chat pipeline.
type Deps struct {
	Cfg   *config.Config
	Store *store.Store
	Auth  *auth.Auth
	Chat  *chat.Service
}

// ddl is kb's own schema (FEATURES-V2 §BE-RAG). Executed idempotently by
// Init; the store's migration files stay untouched.
var ddl = []string{
	`CREATE TABLE IF NOT EXISTS kb_collections (
		id         TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		name       TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS kb_collections_user_idx ON kb_collections (user_id)`,
	`CREATE TABLE IF NOT EXISTS kb_files (
		id            TEXT PRIMARY KEY,
		collection_id TEXT NOT NULL REFERENCES kb_collections (id) ON DELETE CASCADE,
		name          TEXT NOT NULL,
		mime          TEXT NOT NULL,
		bytes         INTEGER NOT NULL,
		created_at    INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS kb_files_collection_idx ON kb_files (collection_id)`,
	`CREATE TABLE IF NOT EXISTS kb_chunks (
		id        TEXT PRIMARY KEY,
		file_id   TEXT NOT NULL REFERENCES kb_files (id) ON DELETE CASCADE,
		ordinal   INTEGER NOT NULL,
		content   TEXT NOT NULL,
		embedding BLOB NOT NULL,
		dim       INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS kb_chunks_file_idx ON kb_chunks (file_id)`,
}

// Init creates kb's tables on the shared store handle. Idempotent.
func Init(st *store.Store) error {
	for _, stmt := range ddl {
		if _, err := st.DB.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Register mounts the /api/kb surface and (when Deps.Chat is set) registers
// the KB enricher with the chat pipeline. It runs Init first; a DDL failure
// is a broken boot, so Register panics rather than serving half a feature.
func Register(mux *http.ServeMux, d *Deps) {
	if err := Init(d.Store); err != nil {
		panic("kb: create tables: " + err.Error())
	}
	registerRoutes(mux, d)
	if d.Chat != nil {
		d.Chat.RegisterEnricher("kb", NewEnricher(d))
	}
}

// registerRoutes mounts every /api/kb handler. The integrator's auth policy
// wraps /api/* with auth.Require; handlers still re-check UserFrom and 401
// defensively (mirrors apichat).
func registerRoutes(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/kb", d.handleCollectionCreate)
	mux.HandleFunc("GET /api/kb", d.handleCollectionList)
	mux.HandleFunc("GET /api/kb/{id}", d.handleCollectionGet)
	mux.HandleFunc("DELETE /api/kb/{id}", d.handleCollectionDelete)
	mux.HandleFunc("POST /api/kb/{id}/files", d.handleFileUpload)
	mux.HandleFunc("GET /api/kb/{id}/files", d.handleFileList)
	mux.HandleFunc("DELETE /api/kb/{id}/files/{fileId}", d.handleFileDelete)
	mux.HandleFunc("POST /api/kb/{id}/url", d.handleURLIngest)
	mux.HandleFunc("POST /api/kb/{id}/search", d.handleSearch)
}

// --- shared helpers ----------------------------------------------------------

// httpError carries a client-safe status/message through helper layers
// (same pattern as apichat's).
type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func httpErr(status int, msg string) error { return httpError{status: status, msg: msg} }

// fail maps kb errors onto contract status codes.
func (d *Deps) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he httpError
	switch {
	case errors.As(err, &he):
		httpx.ErrorJSON(w, he.status, he.msg)
	case errors.Is(err, ErrNotFound):
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrNoEmbedModel):
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrEmbedUpstream):
		// Clean toast-worthy message: no upstream URL, key or body detail.
		slog.Warn("kb: embeddings provider failure", "path", r.URL.Path, "err", err)
		httpx.ErrorJSON(w, http.StatusBadGateway,
			"embeddings provider failed — check the embedding provider and model (details in the server log)")
	default:
		httpx.InternalError(w, r, err)
	}
}

// requireUser answers 401 when no session user is attached.
func (d *Deps) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	if d.Auth != nil {
		if u, ok := d.Auth.UserFrom(r.Context()); ok && u != nil && u.ID != "" {
			return u, true
		}
	}
	httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
	return nil, false
}
