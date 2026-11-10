package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestPlaceholderEmbedded(t *testing.T) {
	rec := get(t, Handler(FS()), "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "LydiaBox") {
		t.Fatalf("placeholder: %d %s", rec.Code, rec.Body)
	}
}

func TestSPAHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":         {Data: []byte("<html>app</html>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
	}
	h := Handler(fsys)
	for _, p := range []string{"/", "/index.html", "/studio", "/files/deep/route"} {
		rec := get(t, h, p)
		if rec.Code != 200 || rec.Body.String() != "<html>app</html>" || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q %v", p, rec.Code, rec.Body, rec.Header())
		}
	}
	rec := get(t, h, "/assets/app-1a2b.js")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", rec.Code, rec.Header())
	}
	if rec := get(t, h, "/favicon.svg"); rec.Code != 200 || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("favicon: %d %v", rec.Code, rec.Header())
	}
	if rec := get(t, h, "/assets/missing.js"); rec.Code != 404 {
		t.Fatalf("missing asset: %d", rec.Code)
	}
	if rec := get(t, h, "/../../etc/passwd"); rec.Code != 200 || rec.Body.String() != "<html>app</html>" {
		t.Fatalf("traversal: %d %q", rec.Code, rec.Body)
	}
}
