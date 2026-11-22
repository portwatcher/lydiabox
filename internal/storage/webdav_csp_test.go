package storage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A guest-written HTML file served through /webdav must not run as active
// content on the host origin.
func TestWebDAVProxySandboxesContent(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	}))
	defer backend.Close()
	proxy := NewWebDAVProxy(strings.TrimPrefix(backend.URL, "http://"), "u", "p")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, WebDAVPrefix+"/workspace/x.html", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("Content-Security-Policy = %q, want sandbox", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
