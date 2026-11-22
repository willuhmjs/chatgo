// Package files stores uploads/artifacts under DataDir/files and builds
// vision payloads. BE-data owns the logic; shapes frozen by docs/CONTRACTS.md.
package files

import (
	"context"
	"errors"
	"io"
	"mime/multipart"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

var errNotImplemented = errors.New("files: not implemented yet (scaffold stub)")

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
// kind is one of store.FileKind*.
func (s *Service) Save(ctx context.Context, userID string, chatID *string, fh *multipart.FileHeader, kind string) (*Meta, error) {
	return nil, errNotImplemented
}

// Get loads a stored file row by id.
func (s *Service) Get(ctx context.Context, id string) (*store.File, error) {
	return nil, errNotImplemented
}

// Open opens the on-disk file for serving. Path is resolved under DataDir
// only (never from user input).
func (s *Service) Open(f *store.File) (io.ReadCloser, error) {
	return nil, errNotImplemented
}

// DataURL renders the original bytes as data:<mime>;base64,… for vision
// payloads (providers.ChatMessage.Images).
func (s *Service) DataURL(ctx context.Context, fileID string) (string, error) {
	return "", errNotImplemented
}
