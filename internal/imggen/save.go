package imggen

// Result persistence: upstream b64 payloads are decoded and pushed through
// the EXISTING files.Service.Save (kind "image"), so generated images get the
// same on-disk layout, MIME sniffing, name sanitization, thumbnail and row
// semantics as uploads — and serve unchanged from /files/{id}/{name}.
//
// CONTRACT-DEVIATION (noted in the report): files.Service.Save only accepts a
// *multipart.FileHeader, so the bytes are round-tripped through an in-memory
// multipart form. Cost is one transient copy of an already size-capped payload
// (≤20 MiB/file, ≤4 files/request); the alternative (duplicating Save's
// storage rules inside imggen) forked the files layout. files/** is not in
// this agent's ownership set, so the shim stays here.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// canonical image MIME → path extension (mirrors files.canonicalExts for the
// image subset; Save's sniffer is authoritative for the stored mime).
var imageExts = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// decodeB64 accepts std (padded or unpadded) base64, the spellings OpenAI-
// compatible upstreams emit.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Cheap pre-check: base64 never expands beyond 4/3 + padding.
	if int64(len(s)) > (maxDecodedImage/3)*4+8 {
		return nil, fmt.Errorf("imggen: image payload exceeds %d MiB cap", maxDecodedImage>>20)
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return nil, errors.New("imggen: image payload is not valid base64")
}

// imageMime sniffs the decoded bytes; anything not an accepted image type is
// rejected (we never store opaque upstream bytes).
func imageMime(data []byte) (string, error) {
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp", nil // DetectContentType has no webp
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	if !strings.HasPrefix(base, "image/") {
		return "", fmt.Errorf("imggen: upstream returned %s, not an image", base)
	}
	if _, ok := imageExts[base]; !ok {
		return "", fmt.Errorf("imggen: unsupported image type %s", base)
	}
	return base, nil
}

// saveImage persists one decoded image via files.Service and returns the
// client-facing shape.
func (h *handler) saveImage(ctx context.Context, userID string, idx int, b64 string) (*imageOut, error) {
	raw, err := decodeB64(b64)
	if err != nil {
		return nil, err
	}
	mime, err := imageMime(raw)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("generated-%d.%s", idx+1, imageExts[mime])

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(raw); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/files", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Threshold ≥ body → the form is parsed fully in memory (no temp files).
	if err := req.ParseMultipartForm(int64(buf.Len()) + 1<<20); err != nil {
		return nil, err
	}
	fhs := req.MultipartForm.File["file"]
	if len(fhs) != 1 {
		return nil, errors.New("imggen: internal multipart shim failed")
	}

	meta, err := h.d.Files.Save(ctx, userID, nil, fhs[0], store.FileKindImage)
	if err != nil {
		return nil, err
	}
	return &imageOut{ID: meta.ID, Name: meta.Name, Mime: meta.Mime, Size: meta.Size, URL: meta.URL}, nil
}

// ---------- GET /api/images ----------

// handleList returns the caller's own generated images, newest first (50 —
// endpoint spec; the repo page cap keeps it bounded regardless).
func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	files, _, err := h.d.Store.Files.ListByUser(r.Context(), u.ID, store.FileKindImage, 1, listRecentLength)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]imageOut, 0, len(files))
	for _, f := range files {
		out = append(out, imageOut{
			ID: f.ID, Name: f.Name, Mime: f.Mime, Size: f.Size,
			URL: "/files/" + f.ID + "/" + url.PathEscape(f.Name),
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"images": out})
}
