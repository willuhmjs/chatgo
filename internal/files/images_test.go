// Thumbnail pipeline tests (pixel-bomb guard + async worker).

package files

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chatgo/internal/store"
)

// patchPNGSize rewrites a PNG's IHDR width/height and repairs the chunk CRC so
// the forged header is fully valid. Without the CRC fix, image.DecodeConfig
// rejects the file with "invalid checksum" and the pixel-budget gate is never
// reached — the test would pass for the wrong reason.
func patchPNGSize(b []byte, w, h uint32) {
	// PNG layout: 8-byte signature, then IHDR = len(4) + "IHDR"(4) + data(13) + CRC(4).
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
}

// waitThumbs blocks until every queued thumbnail worker has finished.
func waitThumbs() { thumbWG.Wait() }

// A PNG header declaring 20000x20000 (400M px ≈ 1.5GiB RGBA) must be refused
// by the DecodeConfig gate BEFORE any pixel allocation: the upload still
// succeeds and the bytes stay usable, but no thumbnail is attempted.
func TestPixelBombUploadKeptWithoutThumb(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)

	bomb := append([]byte(nil), pngBytes(t, 8, 8)...)
	patchPNGSize(bomb, 20000, 20000) // 400M px, valid header

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

// writeThumb refuses >4M-pixel images with an error (never allocates).
func TestWriteThumbRejectsHugeDimensions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	bomb := append([]byte(nil), pngBytes(t, 2, 2)...)
	patchPNGSize(bomb, 5000, 5000) // 25M px > 4M budget
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

// The gate must decide on the header alone. A 2500x2000 (5M px) IHDR pasted
// onto a 4x4 PNG declares just over the 4M-px budget while carrying only 16 real
// pixels: the refusal names the pixel budget (not a decode error), proving no
// pixels were ever allocated or scanned.
func TestWriteThumbRefusesJustOverBudgetBeforeDecode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	bomb := append([]byte(nil), pngBytes(t, 4, 4)...)
	patchPNGSize(bomb, 2500, 2000) // 5M px > 4M budget, valid header
	if err := os.WriteFile(src, bomb, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.jpg")
	err := writeThumb(src, dst)
	if err == nil {
		t.Fatal("writeThumb(5M px) succeeded, want rejection")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// Just under the budget the image really does decode: 2000x2000 = 4M px is the
// largest area still accepted, so the threshold is exclusive, not off by one.
func TestWriteThumbAcceptsExactlyBudget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	if err := os.WriteFile(src, pngBytes(t, 2000, 2000), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.jpg")
	if err := writeThumb(src, dst); err != nil {
		t.Errorf("writeThumb(4M px): %v", err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Size() == 0 {
		t.Errorf("4M-px thumbnail missing: %v", err)
	}
}
