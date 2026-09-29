package http

import (
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// serveStatic returns a handler that streams a single file from disk.
// Used for the OpenAPI spec and the swagger UI HTML. The file is read on every
// request so hot-edits during development are picked up without a restart.
func serveStatic(relPath, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := relPath
		if !filepath.IsAbs(path) {
			wd, err := os.Getwd()
			if err == nil {
				path = filepath.Join(wd, relPath)
			}
		}
		f, err := os.Open(path) //nolint:gosec // relPath is a package-controlled constant
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()

		st, err := f.Stat()
		modAt := time.Time{}
		if err == nil {
			modAt = st.ModTime()
		}
		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, filepath.Base(path), modAt, f)
	}
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}
