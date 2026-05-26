package admin

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MountStaticUI returns a handler that serves files from `dir` (admin-ui/),
// with SPA fallback: any 404 returns index.html. API paths (anything starting
// with /api/, /auth/, /health) are NOT served by this handler — the admin
// router pattern matches must take precedence in the parent mux.
func MountStaticUI(dir string) http.Handler {
	root, err := filepath.Abs(dir)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "admin-ui dir missing", http.StatusInternalServerError)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		path := filepath.Join(root, clean)
		// path-traversal guard
		if !strings.HasPrefix(path, root) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			path = filepath.Join(root, "index.html")
		}
		w.Header().Set("content-type", contentTypeForExt(filepath.Ext(path)))
		http.ServeFile(w, r, path)
	})
}

// StaticUIExists is a tiny sanity probe so cmd/admin can decide whether to
// mount the static handler at all (when running outside the bundled image).
func StaticUIExists(dir string) bool {
	if dir == "" {
		return false
	}
	idx := filepath.Join(dir, "index.html")
	if _, err := fs.Stat(osFS{}, idx); err == nil {
		return true
	}
	return false
}

type osFS struct{}

func (osFS) Open(name string) (fs.File, error) { return os.Open(name) }

func contentTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	}
	return "application/octet-stream"
}
