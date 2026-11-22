// File endpoints: upload, download and the gallery list. (Integration-owned:
// the contract left the files HTTP surface unassigned; the service layer and
// repo come from BE-data.)
package apichat

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/files"
	"chatgo/internal/store"
)

// registerFiles mounts upload/download/gallery routes.
func registerFiles(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("POST /api/files", d.handleFileUpload)
	mux.HandleFunc("GET /api/files", d.handleFileList)
	mux.HandleFunc("GET /files/{id}/{name}", d.handleFileGet)
}

// fileDTO never exposes the on-disk path.
type fileDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"`
}

func fileToDTO(f *store.File) fileDTO {
	return fileDTO{
		ID: f.ID, Name: f.Name, Mime: f.Mime, Size: f.Size, Kind: f.Kind,
		URL: fmt.Sprintf("/files/%s/%s", f.ID, f.Name), CreatedAt: f.CreatedAt,
	}
}

// handleFileUpload accepts one multipart "file" field (≤20MB, sniffed-mime
// allowlist enforced by files.Service).
func (d *Deps) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(files.MaxUploadSize); err != nil {
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
	var chatID *string
	if c := r.FormValue("chat_id"); c != "" {
		chatID = &c
	}
	kind := store.FileKindArtifact
	switch {
	case chatID != nil:
		kind = store.FileKindUpload
	case strings.HasPrefix(fhs[0].Header.Get("Content-Type"), "image/"):
		// Declared mime only selects the gallery bucket here; the stored
		// content type always comes from server-side sniffing in Save.
		kind = store.FileKindImage
	}
	meta, err := d.Files.Save(r.Context(), u.ID, chatID, fhs[0], kind)
	if err != nil {
		// Save rejects oversize/disallowed input by design; IO errors could
		// contain paths — log server-side, answer generically.
		slog.Warn("files: upload rejected", "user", u.ID, "err", err)
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid or unsupported file")
		return
	}
	httpx.JSON(w, http.StatusCreated, meta)
}

// handleFileList powers the gallery: GET /api/files?kind=&page=&per=.
func (d *Deps) handleFileList(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per"))
	if per < 1 {
		per = 30
	}
	if per > 100 {
		per = 100
	}
	rows, total, err := d.Store.Files.ListByUser(r.Context(), u.ID, kind, page, per)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	items := make([]fileDTO, 0, len(rows))
	for _, f := range rows {
		items = append(items, fileToDTO(f))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "per": per})
}

// handleFileGet serves the original bytes to the owner only (404 for anyone
// else, per the no-existence-leak policy).
func (d *Deps) handleFileGet(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	f, err := d.Files.Get(r.Context(), r.PathValue("id"))
	if err != nil || f.UserID != u.ID {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	rc, err := d.Files.Open(f)
	if err != nil {
		slog.Warn("files: open failed", "id", f.ID, "err", err)
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", f.Name))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if f.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}
