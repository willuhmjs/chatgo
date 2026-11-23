package files

// Thumbnails + display-name sanitization (BE-data, contract §files):
//   - 512px longest-edge .thumb jpeg for png/jpeg via golang.org/x/image
//     (other formats — and undecodable images — skip the thumb silently)
//   - decode is dimension-gated: image.DecodeConfig runs FIRST and images
//     declaring >4M pixels are refused before any pixel allocation
//     (pixel-bomb guard — a <1MB PNG can expand to GiBs of RGBA)
//   - thumbnail generation is ASYNC behind a 2-slot semaphore so upload
//     requests never block and never hold decode memory on the hot path;
//     when both slots are busy new thumbs are skipped (best-effort + logged)
//   - Sanitize display names: filepath.Base + control-char strip + length cap

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	_ "image/png" // register the png decoder for image.Decode

	"golang.org/x/image/draw"
)

const (
	// thumbMaxEdge is the longest-edge pixel budget for generated thumbnails.
	thumbMaxEdge = 512
	// maxThumbPixels is the hard decode ceiling for thumbnail work: an image
	// whose header declares more pixels is rejected before image.Decode.
	// Budget: 4M px × 4 B/px RGBA = 16 MiB per decode; with both thumbSem
	// slots busy that is 32 MiB of theoretical peak decode memory.
	maxThumbPixels = 4 << 20
	// maxNameLen caps sanitized display names.
	maxNameLen = 200
)

var (
	// thumbSem bounds concurrent thumbnail decodes (≤2 × ≤4M-px RGBA = 32 MiB).
	thumbSem = make(chan struct{}, 2)
	// thumbWG lets tests drain in-flight thumbnail workers.
	thumbWG sync.WaitGroup
)

// thumbPathFor maps a stored file path (…/<uuid>.<ext>) to its thumbnail path
// (…/<uuid>.thumb.jpg).
func thumbPathFor(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".thumb.jpg"
}

// queueThumb schedules writeThumb on a background worker (best-effort). When
// both semaphore slots are busy the thumbnail is skipped rather than queued:
// uploads never block and pending decode memory stays bounded.
func queueThumb(srcPath, dstPath string) {
	select {
	case thumbSem <- struct{}{}:
	default:
		slog.Debug("files: thumbnail workers busy, skipping", "src", srcPath)
		return
	}
	thumbWG.Add(1)
	go func() {
		defer thumbWG.Done()
		defer func() { <-thumbSem }()
		if err := writeThumb(srcPath, dstPath); err != nil {
			slog.Debug("files: thumbnail skipped", "src", srcPath, "err", err)
		}
	}()
}

// writeThumb decodes srcPath (png/jpeg only — everything else is skipped)
// and writes a JPEG thumbnail whose longest edge is ≤ thumbMaxEdge. Headers
// are inspected (DecodeConfig) before any pixel allocation. Best-effort: it
// returns a descriptive error and never leaves a partial file behind.
func writeThumb(srcPath, dstPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return err // decode errors / non-images skip the thumb
	}
	if format != "png" && format != "jpeg" {
		return nil // unsupported formats skip the thumb silently
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("files: bogus image size %dx%d", cfg.Width, cfg.Height)
	}
	if px := int64(cfg.Width) * int64(cfg.Height); px > maxThumbPixels {
		return fmt.Errorf("files: image %dx%d (%d px) exceeds %d-px decode budget",
			cfg.Width, cfg.Height, px, maxThumbPixels)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return fmt.Errorf("files: empty image bounds %dx%d", w, h)
	}

	dw, dh := w, h
	if longest := max(w, h); longest > thumbMaxEdge {
		if w >= h {
			dw = thumbMaxEdge
			dh = max(1, h*thumbMaxEdge/w)
		} else {
			dh = thumbMaxEdge
			dw = max(1, w*thumbMaxEdge/h)
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)

	out, cerr := os.Create(dstPath)
	if cerr != nil {
		return cerr
	}
	if eerr := jpeg.Encode(out, dst, &jpeg.Options{Quality: 82}); eerr != nil {
		out.Close()
		os.Remove(dstPath)
		return eerr
	}
	if cerr := out.Close(); cerr != nil {
		os.Remove(dstPath)
		return cerr
	}
	return nil
}

// sanitizeName turns a user-supplied multipart filename into a safe display
// name: filepath.Base (also strips Windows path separators), control
// characters replaced, whitespace/dots trimmed, length-capped.
func sanitizeName(name string) string {
	n := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, n)
	n = strings.TrimSpace(strings.Trim(strings.TrimSpace(n), "."))
	if r := []rune(n); len(r) > maxNameLen {
		n = string(r[:maxNameLen])
	}
	if n == "" {
		return "file"
	}
	return n
}
