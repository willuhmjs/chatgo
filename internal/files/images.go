package files

// Thumbnails + display-name sanitization (BE-data, contract §files):
//   - 512px longest-edge .thumb jpeg for png/jpeg via golang.org/x/image
//     (other formats — and undecodable images — skip the thumb silently)
//   - Sanitize display names: filepath.Base + control-char strip + length cap

import (
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	_ "image/png" // register the png decoder for image.Decode

	"golang.org/x/image/draw"
)

const (
	// thumbMaxEdge is the longest-edge pixel budget for generated thumbnails.
	thumbMaxEdge = 512
	// maxNameLen caps sanitized display names.
	maxNameLen = 200
)

// thumbPathFor maps a stored file path (…/<uuid>.<ext>) to its thumbnail path
// (…/<uuid>.thumb.jpg).
func thumbPathFor(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".thumb.jpg"
}

// writeThumb decodes srcPath (png/jpeg only — everything else is skipped
// silently) and writes a JPEG thumbnail whose longest edge is ≤ thumbMaxEdge.
// Thumbnail generation is best-effort: it returns nothing and never leaves a
// partial file behind.
func writeThumb(srcPath, dstPath string) {
	f, oerr := os.Open(srcPath)
	if oerr != nil {
		return
	}
	defer f.Close()

	cfg, format, derr := image.Decode(f)
	if derr != nil || (format != "png" && format != "jpeg") {
		return // decode errors / unsupported formats skip the thumb
	}
	b := cfg.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return
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
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), cfg, b, draw.Src, nil)

	out, cerr := os.Create(dstPath)
	if cerr != nil {
		return
	}
	if eerr := jpeg.Encode(out, dst, &jpeg.Options{Quality: 82}); eerr != nil {
		out.Close()
		os.Remove(dstPath)
		return
	}
	if cerr := out.Close(); cerr != nil {
		os.Remove(dstPath)
	}
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
