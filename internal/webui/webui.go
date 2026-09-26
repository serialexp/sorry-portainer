// Package webui serves the SolidJS dashboard that `pnpm build` writes into
// dist/. The page is built into the server binary, so a release is one file
// with no separate web server to run.
package webui

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// A server build needs the page: run `pnpm build` (or `just build`) first.
//
//go:embed all:dist
var embedded embed.FS

// assetCache marks Vite's content-hashed files, which never change under the
// same name.
const assetCache = "public, max-age=31536000, immutable"

// Handler serves the dashboard built into this binary.
func Handler() (http.Handler, error) {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	return newHandler(dist)
}

type handler struct{ files fs.FS }

func newHandler(files fs.FS) (http.Handler, error) {
	if _, err := fs.Stat(files, "index.html"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errors.New("the dashboard build has no index.html; run `pnpm build` before building the server")
		}
		return nil, fmt.Errorf("read the dashboard build: %w", err)
	}
	return handler{files: files}, nil
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" {
		if info, err := fs.Stat(h.files, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", assetCache)
			}
			http.ServeFileFS(w, r, h.files, name)
			return
		}
		// A missing script or stylesheet must fail, not come back as HTML.
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
	}
	// Every other path is a dashboard route, which the page's router resolves.
	// The page is always revalidated so a new release is picked up at once.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, h.files, "index.html")
}
