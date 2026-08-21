package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Assets returns the embedded dist filesystem.
func Assets() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// Available reports whether index.html exists.
func Available() bool {
	a, err := Assets()
	if err != nil {
		return false
	}
	_, err = fs.Stat(a, "index.html")
	return err == nil
}

const placeholder = `Virtualis backend is running but frontend not built.

Run:
  make frontend
  make build
API is available under /api.
`

// Handler serves SPA assets with fallback to index.html.
func Handler() http.HandlerFunc {
	assets, err := Assets()
	if err != nil || !Available() {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(placeholder))
		}
	}
	fileServer := http.FileServer(http.FS(assets))
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			serveIndex(w, r, assets)
			return
		}
		info, statErr := fs.Stat(assets, name)
		if statErr != nil || info.IsDir() {
			serveIndex(w, r, assets)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "frontend missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
