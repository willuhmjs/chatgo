// Package web embeds the built Svelte SPA (web/ → `npm run build` → dist).
// A placeholder dist/index.html is committed so `go build ./...` works
// without a prior npm build; the real build overwrites it.
package web

import (
	"embed"
	"io/fs"
	"mime"
)

//go:embed all:dist
var distFS embed.FS

// init teaches the stdlib mime package about the PWA manifest extension so
// http.FileServer serves it as application/manifest+json (Go's built-in table
// predates it; a wrong MIME makes some browsers ignore the manifest).
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// Dist returns the SPA filesystem rooted at the dist directory.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: "dist" is guaranteed present at compile time by go:embed.
		panic("web: embedded dist missing: " + err.Error())
	}
	return sub
}
