package staticweb

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fs.FS {
	return fstest.MapFS{
		"index.html":             &fstest.MapFile{Data: []byte("<html><body>HappyLearn</body></html>")},
		"assets/app-1a2b3c4d.js": &fstest.MapFile{Data: []byte("console.log('ok')")},
		"assets/app.css":         &fstest.MapFile{Data: []byte("body{}")},
		".env":                   &fstest.MapFile{Data: []byte("SECRET=never")},
	}
}

func TestUnknownAPIRouteDoesNotReturnSPA(t *testing.T) {
	h := New(testFS())
	r := httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<html") {
		t.Fatal(w.Body.String())
	}
}

func TestAPIMissAlwaysUsesJSONNotFoundContract(t *testing.T) {
	h := New(testFS())
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/missing", nil))
			if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || strings.Contains(w.Body.String(), "<html") {
				t.Fatalf("status=%d content-type=%q body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
		})
	}
}
func TestClientRouteReturnsIndex(t *testing.T) {
	h := New(testFS())
	r := httptest.NewRequest(http.MethodGet, "/admin/students", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
		t.Fatal(w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control = %q", got)
	}
}

func TestStaticHandlerProtectsFilesAndMethods(t *testing.T) {
	h := New(testFS())
	for _, path := range []string{"/.env", "/assets/.env", "/../index.html", "/%2e%2e/index.html"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "SECRET") {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/students", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("status=%d allow=%q", w.Code, w.Header().Get("Allow"))
	}
}

func TestStaticHandlerCachesHashedAssetsAndSupportsHEAD(t *testing.T) {
	h := New(testFS())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app-1a2b3c4d.js", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "public,max-age=31536000,immutable" {
		t.Fatalf("status=%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
	}
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/assets/app-1a2b3c4d.js", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", head.Code, head.Body.String())
	}
}
