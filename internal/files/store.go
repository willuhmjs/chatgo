// Package files stores uploads/artifacts under DataDir/files and builds
// vision payloads. BE-data owns the logic; shapes frozen by docs/CONTRACTS.md.
package files

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

const (
	// MaxUploadSize is the hard per-file cap (contract: multipart bodies ≤20MB).
	MaxUploadSize int64 = 20 << 20
	// maxDataURLImage bounds vision payloads built from original bytes.
	maxDataURLImage int64 = 8 << 20
	// sniffBytes is what http.DetectContentType looks at.
	sniffBytes = 512
	// filesSubdir is the folder under DataDir holding stored bytes.
	filesSubdir = "files"
)

// allowedMimes is the upload allowlist (contract §HTTP surface). The sniffed
// type must canonicalize into this set; filename extensions never matter.
var allowedMimes = map[string]bool{
	"image/png":        true,
	"image/jpeg":       true,
	"image/webp":       true,
	"image/gif":        true,
	"application/pdf":  true,
	"text/plain":       true,
	"text/markdown":    true,
	"text/csv":         true,
	"application/json": true,
}

// canonicalExts map allowed mimes to the path suffix used when the upload's
// own filename carries no usable extension.
var canonicalExts = map[string]string{
	"image/png":        "png",
	"image/jpeg":       "jpg",
	"image/webp":       "webp",
	"image/gif":        "gif",
	"application/pdf":  "pdf",
	"text/plain":       "txt",
	"text/markdown":    "md",
	"text/csv":         "csv",
	"application/json": "json",
}

var errTooLarge = fmt.Errorf("files: exceeds %d-byte upload limit", MaxUploadSize)

// Meta is the FileMeta JSON returned by POST /api/files.
type Meta struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	Kind string `json:"kind"`
	URL  string `json:"url"` // /files/{id}/{name}
}

// Service persists files at DataDir/files/<uuid>.<ext> (images get a 512px
// .thumb jpeg; see images.go).
type Service struct {
	Cfg   *config.Config
	Store *store.Store
}

// New builds the files service.
func New(cfg *config.Config, st *store.Store) *Service {
	return &Service{Cfg: cfg, Store: st}
}

// Save validates (≤20MB, allowed mimes), writes and records a file row.
// kind is one of store.FileKind*. Content type comes from sniffing the first
// 512 bytes — the filename extension is purely cosmetic (path suffix only) and
// the declared multipart Content-Type is only consulted to tell plain text
// apart from markdown/csv/json (sniffing cannot).
func (s *Service) Save(ctx context.Context, userID string, chatID *string, fh *multipart.FileHeader, kind string) (*Meta, error) {
	if fh == nil {
		return nil, errors.New("files: no file header")
	}
	switch kind {
	case store.FileKindUpload, store.FileKindArtifact, store.FileKindImage:
	default:
		return nil, fmt.Errorf("files: unknown kind %q", kind)
	}
	if fh.Size > MaxUploadSize {
		return nil, errTooLarge
	}

	src, err := fh.Open()
	if err != nil {
		return nil, fmt.Errorf("files: open upload: %w", err)
	}
	defer src.Close()

	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("files: read upload: %w", err)
	}
	head = head[:n]

	mime, err := sniffContentType(head, fh.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(s.Cfg.DataDir, filesSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("files: mkdir %s: %w", dir, err)
	}
	id := uuid.NewString()
	name := sanitizeName(fh.Filename)
	tmp := filepath.Join(dir, id+".part")
	final := filepath.Join(dir, id+"."+extFor(name, mime))

	written, err := writeCapped(tmp, head, src)
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("files: store %s: %w", id, err)
	}

	rel := filesSubdir + "/" + filepath.Base(final)
	rec := &store.File{
		ID:     id,
		UserID: userID,
		ChatID: chatID,
		Name:   name,
		Mime:   mime,
		Size:   written,
		Path:   rel,
		Kind:   kind,
	}
	if err := s.Store.Files.Insert(ctx, rec); err != nil {
		os.Remove(final)
		return nil, fmt.Errorf("files: record %s: %w", id, err)
	}

	// Best-effort 512px thumbnail for png/jpeg (contract §files).
	if mime == "image/png" || mime == "image/jpeg" {
		writeThumb(final, thumbPathFor(final))
	}

	return &Meta{
		ID:   rec.ID,
		Name: rec.Name,
		Mime: rec.Mime,
		Size: rec.Size,
		Kind: rec.Kind,
		URL:  "/files/" + rec.ID + "/" + url.PathEscape(rec.Name),
	}, nil
}

// writeCapped writes head+rest to path (.part style: caller renames),
// enforcing MaxUploadSize even if fh.Size lied. Returns bytes written.
func writeCapped(path string, head []byte, rest io.Reader) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, fmt.Errorf("files: create temp: %w", err)
	}
	buf := bufio.NewWriter(f)
	buf.Write(head) // flush/close below surface any write error
	w, err := io.Copy(buf, io.LimitReader(rest, MaxUploadSize+1-int64(len(head))))
	w += int64(len(head))
	ferr := buf.Flush()
	cerr := f.Close()
	switch {
	case err != nil:
		return w, fmt.Errorf("files: write temp: %w", err)
	case ferr != nil:
		return w, fmt.Errorf("files: write temp: %w", ferr)
	case cerr != nil:
		return w, fmt.Errorf("files: close temp: %w", cerr)
	}
	if w > MaxUploadSize {
		return w, errTooLarge
	}
	return w, nil
}

// sniffContentType returns the canonical allowed mime for head (first 512
// bytes), or an error when the content type is not allowed. declared is the
// client's multipart Content-Type; it is NOT trusted, only used as a hint to
// label sniffed plain text as markdown/csv/json.
func sniffContentType(head []byte, declared string) (string, error) {
	if isWebP(head) {
		return "image/webp", nil
	}
	ct := http.DetectContentType(head)
	base := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch base {
	case "image/png", "image/jpeg", "image/gif", "application/pdf":
	case "text/plain":
		// Sniffing cannot tell md/csv/json from plain text; keep the declared
		// subtype when it is one of those (extension is not consulted).
		switch declaredType(declared) {
		case "text/markdown", "text/csv", "application/json":
			base = declaredType(declared)
		}
	}
	if !allowedMimes[base] {
		return "", fmt.Errorf("files: content type %q not allowed", base)
	}
	return base, nil
}

func declaredType(declared string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
}

// isWebP checks the RIFF....WEBP magic (http.DetectContentType has no webp).
func isWebP(head []byte) bool {
	return len(head) >= 12 &&
		string(head[0:4]) == "RIFF" &&
		string(head[8:12]) == "WEBP"
}

// extFor picks the cosmetic path extension: sanitized filename suffix when
// present, else the canonical one for the sniffed mime.
func extFor(sanitizedName, mime string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(sanitizedName), "."))
	if ext == "" || len(ext) > 8 {
		return canonicalExts[mime]
	}
	clean := make([]rune, 0, len(ext))
	for _, r := range ext {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return canonicalExts[mime]
	}
	return string(clean)
}

// Get loads a stored file row by id.
func (s *Service) Get(ctx context.Context, id string) (*store.File, error) {
	return s.Store.Files.Get(ctx, id)
}

// Open opens the on-disk file for serving. Path is resolved under DataDir
// only (never from user input).
func (s *Service) Open(f *store.File) (io.ReadCloser, error) {
	p, err := s.resolvePath(f)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Delete removes the row and then (best effort) the stored bytes + thumbnail.
// Row-first guarantees a failed unlink can never orphan a visible row.
func (s *Service) Delete(ctx context.Context, id string) error {
	f, err := s.Store.Files.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Store.Files.Delete(ctx, id); err != nil {
		return err
	}
	if p, err := s.resolvePath(f); err == nil {
		os.Remove(p)
		os.Remove(thumbPathFor(p))
	}
	return nil
}

// GetThumb opens the generated .thumb.jpg for f (os.ErrNotExist when the file
// is not an image or the thumbnail failed to generate).
func (s *Service) GetThumb(f *store.File) (io.ReadCloser, error) {
	p, err := s.resolvePath(f)
	if err != nil {
		return nil, err
	}
	return os.Open(thumbPathFor(p))
}

// resolvePath maps f.Path (stored relative to DataDir, e.g. "files/<uuid>.png")
// to an absolute path, refusing anything that escapes DataDir/files.
func (s *Service) resolvePath(f *store.File) (string, error) {
	if f == nil || f.Path == "" {
		return "", errors.New("files: empty path")
	}
	p := filepath.Clean(filepath.FromSlash(f.Path))
	if filepath.IsAbs(p) || !strings.HasPrefix(p, filesSubdir+string(filepath.Separator)) {
		return "", fmt.Errorf("files: refusing path %q", f.Path)
	}
	return filepath.Join(s.Cfg.DataDir, p), nil
}

// DataURL renders the original bytes as data:<mime>;base64,… for vision
// payloads (providers.ChatMessage.Images). Images only, ≤8MB originals.
func (s *Service) DataURL(ctx context.Context, fileID string) (string, error) {
	f, err := s.Store.Files.Get(ctx, fileID)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(f.Mime, "image/") {
		return "", fmt.Errorf("files: %s is %s, not an image", fileID, f.Mime)
	}
	rc, err := s.Open(f)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxDataURLImage+1))
	if err != nil {
		return "", fmt.Errorf("files: read %s: %w", fileID, err)
	}
	if int64(len(data)) > maxDataURLImage {
		return "", fmt.Errorf("files: %s exceeds %d-byte image limit for vision payloads", fileID, maxDataURLImage)
	}
	return "data:" + f.Mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
