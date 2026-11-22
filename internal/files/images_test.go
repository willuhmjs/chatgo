// Thumbnail pipeline tests (pixel-bomb guard + async worker).

package files

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"chatgo/internal/store"
)

// waitThumbs blocks until every queued thumbnail worker has finished.
func waitThumbs() { thumbWG.Wait() }

// A PNG header declaring 20000x20000 (400M px ≈ 1.5GiB RGBA) must be refused
// by the DecodeConfig gate BEFORE any pixel allocation: the upload still
// succeeds and the bytes stay usable, but no thumbnail is attempted.
func TestPixelBombUploadKeptWithoutThumb(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)

	bomb := append([]byte(nil), pngBytes(t, 8, 8)...)
	// IHDR width/height live at bytes 16..24 of a PNG, big-endian.
	binary.BigEndian.PutUint32(bomb[16:], 20000)
	binary.BigEndian.PutUint32(bomb[20:], 20000)

	meta, err := svc.Save(context.Background(), uid, nil,
		fileHeader(t, "bomb.png", "image/png", bomb), store.FileKindImage)
	if err != nil {
		t.Fatalf("Save(pixel bomb) must succeed, got: %v", err)
	}
	waitThumbs()

	thumb := filepath.Join(dir, "files", meta.ID+".thumb.jpg")
	if _, err := os.Stat(thumb); !os.IsNotExist(err) {
		t.Errorf("pixel bomb produced a thumbnail: %v", err)
	}
	row, err := svc.Get(context.Background(), meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rc, err := svc.Open(row)
	if err != nil {
		t.Fatalf("Open(bomb): %v", err)
	}
	defer rc.Close()
}

// Thumbnails are generated asynchronously; after the worker drains, a normal
// png upload has its 512px thumb exactly like the old synchronous path.
func TestThumbGeneratedAsync(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)

	meta, err := svc.Save(context.Background(), uid, nil,
		fileHeader(t, "t.png", "image/png", pngBytes(t, 600, 300)), store.FileKindImage)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	waitThumbs()
	if _, err := os.Stat(filepath.Join(dir, "files", meta.ID+".thumb.jpg")); err != nil {
		t.Errorf("async thumbnail missing after drain: %v", err)
	}
}

// writeThumb refuses >16M-pixel images with an error (never allocates).
func TestWriteThumbRejectsHugeDimensions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	bomb := append([]byte(nil), pngBytes(t, 2, 2)...)
	binary.BigEndian.PutUint32(bomb[16:], 5000)
	binary.BigEndian.PutUint32(bomb[20:], 5000) // 25M px > 16M budget
	if err := os.WriteFile(src, bomb, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.jpg")
	if err := writeThumb(src, dst); err == nil {
		t.Error("writeThumb(25M px) succeeded, want rejection")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("rejected thumb left a file behind")
	}
	// Small images still succeed.
	if err := os.WriteFile(src, pngBytes(t, 40, 20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeThumb(src, dst); err != nil {
		t.Errorf("writeThumb(40x20): %v", err)
	}
}
