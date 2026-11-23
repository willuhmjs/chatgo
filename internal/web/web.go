// Package web embeds the built Svelte SPA (web/ → `npm run build` → dist).
// A placeholder dist/index.html is committed so `go build ./...` works
// without a prior npm build; the real build overwrites it.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist returns the SPA filesystem rooted at the dist directory.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: "dist" is guaranteed present at compile time by go:embed.
		panic("web: embedded dist missing: " + err.Error())
	}
	return sub
}
