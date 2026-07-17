// Package staticweb serves the browser console without allowing it to mask API errors.
package staticweb

import (
	"bytes"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"happylearn.local/app/internal/platform/httpx"
)

var hashedAsset = regexp.MustCompile(`-[0-9a-fA-F]{8,}\.`)

// Handler safely serves a Vite build from files. API paths intentionally never
// fall back to index.html, so a client cannot mistake an API miss for success.
type Handler struct{ files fs.FS }

func New(files fs.FS) *Handler { return &Handler{files: files} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	name, valid := safeName(r.URL.Path)
	if !valid {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(h.files, name)
	if err != nil {
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		data, err = fs.ReadFile(h.files, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if hashedAsset.MatchString(path.Base(name)) {
		w.Header().Set("Cache-Control", "public,max-age=31536000,immutable")
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func safeName(requestPath string) (string, bool) {
	if requestPath == "" || requestPath == "/" {
		return "index.html", true
	}
	trimmed := strings.TrimPrefix(requestPath, "/")
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	if !fs.ValidPath(trimmed) {
		return "", false
	}
	return trimmed, true
}
