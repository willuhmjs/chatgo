package kb

// HTTP surface (FEATURES-V2 §BE-RAG): collections CRUD, multipart text
// ingest, file listing/removal and scored search, all under /api/kb and all
// private per user (foreign rows answer 404).

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// textExts is the ingest allowlist: text formats + common code/script
// extensions (FEATURES-V2: "txt/md/csv/json/log/code"). Anything else —
// notably PDF/images — is rejected 415 (stdlib-only: no PDF parsing).
var textExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".mdx": true, ".rst": true,
	".csv": true, ".tsv": true, ".json": true, ".jsonl": true, ".ndjson": true,
	".log": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true,
	".cfg": true, ".conf": true, ".xml": true, ".html": true, ".htm": true,
	".css": true, ".scss": true, ".tex": true, ".sql": true, ".proto": true,
	".go": true, ".py": true, ".js": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true, ".jsx": true, ".vue": true, ".svelte": true,
	".rs": true, ".java": true, ".kt": true, ".scala": true, ".c": true,
	".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true,
	".rb": true, ".php": true, ".swift": true, ".sh": true, ".bash": true,
	".zsh": true, ".fish": true, ".ps1": true, ".r": true, ".lua": true,
	".pl": true, ".pm": true, ".dart": true, ".ex": true, ".exs": true,
	".hs": true, ".ml": true, ".clj": true, ".tf": true, ".gradle": true,
	".mk": true, ".cmake": true, ".make": true,
}

// --- DTOs --------------------------------------------------------------

type collectionDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	FileCount  int    `json:"file_count"`
	ChunkCount int    `json:"chunk_count"`
}

type fileDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	Chunks    int    `json:"chunks"`
	CreatedAt int64  `json:"created_at"`
}

func fileToDTO(f *kbFile) fileDTO {
	return fileDTO{ID: f.ID, Name: f.Name, Mime: f.Mime, Bytes: f.Bytes, Chunks: f.ChunkCount, CreatedAt: f.CreatedAt}
}

func collectionToDTO(c *collection, files, chunks int) collectionDTO {
	return collectionDTO{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt, FileCount: files, ChunkCount: chunks}
}

// --- rate limits (SECREV-V2 L7-M1 / lens 8: kb spend) ---------------------
//
// Uploads and searches trigger PAID embeddings-provider calls, so every
// spend-bearing kb route goes through the shared keyed auth limiter
// (Auth.Allow — same helper as login/imggen; burst = rate/min, refill at
// 60/r seconds). Numbers:
//
//   - uploadPerMin = 5/min/user: each upload already batches up to 8 files
//     (≤5 MiB each), so 5 admits a generous interactive import burst
//     (40 files/min) while capping scripted embedding spend at a constant
//     fraction of what an unauthenticated flood could burn. Matches the
//     house 5/min magnitude (loginPerMin, imggen's family).
//   - searchPerMin = 20/min/user: one paid embed call per query; 20/min is
//     far above interactive RAG use. Chat-turn enrichment does NOT pass
//     through this route (it runs inside the chat send, bounded there).
//   - createPerMin = 10/min/user: collection rows are DB-only (no provider
//     spend); this is a human-scale cap against table bloat, kept because
//     it is a one-line reuse of the same gate.
const (
	uploadPerMin = 5
	searchPerMin = 20
	createPerMin = 10
)

// allowSpend consumes one token from a per-user bucket and answers the
// house 429 envelope when empty. requireUser guarantees a live Auth, so the
// limiter is always present here.
func (d *Deps) allowSpend(w http.ResponseWriter, u *store.User, bucket string, perMin float64, msg string) bool {
	if d.Auth.Allow(bucket+u.ID, perMin) {
		return true
	}
	httpx.ErrorJSON(w, http.StatusTooManyRequests, msg)
	return false
}

// --- collections ---------------------------------------------------------

func (d *Deps) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	if !d.allowSpend(w, u, "kb:create:", createPerMin, "too many collection creations, try again later") {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 120 {
		httpx.ErrorJSON(w, http.StatusBadRequest, "name is required (max 120 characters)")
		return
	}
	c, err := d.createCollection(r.Context(), u.ID, name)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, collectionToDTO(c, 0, 0))
}

func (d *Deps) handleCollectionList(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per"))
	rows, total, err := d.listCollections(r.Context(), u.ID, page, per)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	items := make([]collectionDTO, 0, len(rows))
	for _, row := range rows {
		c := row.Collection
		items = append(items, collectionToDTO(&c, row.FileCount, row.ChunkCount))
	}
	if page < 1 {
		page = 1
	}
	if per <= 0 {
		per = 30
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "per": per})
}

func (d *Deps) handleCollectionGet(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, files, err := d.ownCollectionFiles(r, u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var chunks int
	items := make([]fileDTO, 0, len(files))
	for i := range files {
		chunks += files[i].ChunkCount
		items = append(items, fileToDTO(&files[i]))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"collection": collectionToDTO(c, len(files), chunks),
		"files":      items,
	})
}

func (d *Deps) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	if err := d.deleteCollection(r.Context(), u.ID, r.PathValue("id")); err != nil {
		d.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownCollectionFiles resolves {id} with ownership and lists its files.
func (d *Deps) ownCollectionFiles(r *http.Request, userID string) (*collection, []kbFile, error) {
	c, err := d.getCollection(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		return nil, nil, err
	}
	files, err := d.listFiles(r.Context(), c.ID)
	if err != nil {
		return nil, nil, err
	}
	return c, files, nil
}

// --- files (multipart ingest) ---------------------------------------------

func (d *Deps) handleFileList(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	if _, files, err := d.ownCollectionFiles(r, u.ID); err != nil {
		d.fail(w, r, err)
		return
	} else {
		items := make([]fileDTO, 0, len(files))
		for i := range files {
			items = append(items, fileToDTO(&files[i]))
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func (d *Deps) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.getCollection(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	f, err := d.getFile(r.Context(), c.ID, r.PathValue("fileId"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if err := d.deleteFile(r.Context(), f.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleFileUpload accepts multipart field "file" (repeatable, ≤8 parts):
// extension allowlist, 5 MiB/file cap (io.LimitReader on top of the declared
// size), binary rejection, chunking and embedding. Files embed and persist
// one at a time (RAM discipline); a failure surfaces its status and leaves
// earlier files of the same request persisted (re-uploading just the failed
// file is the retry path).
func (d *Deps) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	// Spend gate BEFORE any multipart parsing/embedding (L7-M1): every
	// accepted file costs provider credits.
	if !d.allowSpend(w, u, "kb:upload:", uploadPerMin, "too many kb uploads, try again later") {
		return
	}
	c, err := d.getCollection(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxFileBytes*maxFilesPerRequest+multipartSlack)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "upload too large or malformed multipart body")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	fhs := r.MultipartForm.File["file"]
	if len(fhs) == 0 {
		httpx.ErrorJSON(w, http.StatusBadRequest, `multipart field "file" is required`)
		return
	}
	if len(fhs) > maxFilesPerRequest {
		httpx.ErrorJSON(w, http.StatusBadRequest, "too many files in one request (max 8)")
		return
	}
	created := make([]fileDTO, 0, len(fhs))
	for _, fh := range fhs {
		f, err := d.ingestUpload(r.Context(), c.ID, fh)
		if err != nil {
			d.fail(w, r, err) // partial-import note is in the log; client sees the first failure
			return
		}
		created = append(created, fileToDTO(f))
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"items": created})
}

// ingestUpload validates + chunks + embeds + persists one uploaded file.
func (d *Deps) ingestUpload(ctx context.Context, collectionID string, fh *multipart.FileHeader) (*kbFile, error) {
	name := sanitizeName(fh.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	if !textExts[ext] {
		return nil, httpErr(http.StatusUnsupportedMediaType,
			"unsupported file type "+strconv.Quote(ext)+" — accept txt/md/csv/json/log and source-code extensions")
	}
	if fh.Size > MaxFileBytes {
		return nil, httpErr(http.StatusRequestEntityTooLarge, "file exceeds the 5 MiB limit")
	}
	src, err := fh.Open()
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "cannot read upload")
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, MaxFileBytes+1))
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "cannot read upload")
	}
	if int64(len(data)) > MaxFileBytes {
		return nil, httpErr(http.StatusRequestEntityTooLarge, "file exceeds the 5 MiB limit")
	}
	mime, err := classifyText(data)
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	chunks := chunkText(text)

	var vectors [][]float32
	if len(chunks) > 0 {
		target, err := d.pickEmbedModel(ctx)
		if err != nil {
			return nil, err
		}
		vectors, err = d.embedTexts(ctx, target, chunks)
		if err != nil {
			return nil, err
		}
	}
	return d.insertFileChunks(ctx, collectionID, name, mime, int64(len(data)), chunks, vectors)
}

// classifyText rejects binary payloads (415) and returns a display mime.
// Gate 1: NUL bytes in the leading sniff window; gate 2: http.DetectContentType
// must report text/* (or JSON); gate 3: the WHOLE payload must be valid UTF-8
// (binary after the first 512 bytes is caught here).
func classifyText(data []byte) (string, error) {
	reject := httpErr(http.StatusUnsupportedMediaType, "binary or non-UTF-8 content is not supported (text files only)")
	if len(bytes.TrimSpace(data)) == 0 {
		return "text/plain", nil
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", reject
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(head), ";")[0]))
	if !strings.HasPrefix(base, "text/") && base != "application/json" {
		return "", reject
	}
	if !utf8.Valid(data) {
		return "", reject
	}
	return base, nil
}

// sanitizeName mirrors files.Service's display-name hygiene.
func sanitizeName(raw string) string {
	n := filepath.Base(strings.ReplaceAll(raw, "\\", "/"))
	n = strings.TrimSpace(n)
	if n == "" || n == "." || n == "/" || n == string(filepath.Separator) {
		n = "upload"
	}
	if len([]rune(n)) > 200 {
		n = string([]rune(n)[:200])
	}
	return n
}

// --- search --------------------------------------------------------------

func (d *Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	// Spend gate before the query embed + brute scan (L7-M1: embedding
	// credits AND scan CPU per user).
	if !d.allowSpend(w, u, "kb:search:", searchPerMin, "too many kb searches, try again later") {
		return
	}
	c, err := d.getCollection(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var in struct {
		Q    string `json:"q"`
		TopK int    `json:"top_k"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "q is required")
		return
	}
	k := in.TopK
	if k <= 0 {
		k = searchDefaultK
	}
	if k > searchMaxK {
		k = searchMaxK
	}
	hits, err := d.searchChunks(r.Context(), u.ID, []string{c.ID}, q, k)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if hits == nil {
		hits = []chunkHit{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": hits})
}
