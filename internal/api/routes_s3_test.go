package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/storage"
)

// TestS3RouteForwardsVerbatim checks that /s3 reaches the gateway backend
// without API authentication and with every SigV4 input unchanged: method,
// escaped path, query, Host, Authorization, and x-amz-* headers.
func TestS3RouteForwardsVerbatim(t *testing.T) {
	type seen struct {
		method, path, query, host, auth, date, sha, body string
	}
	got := make(chan seen, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, r.Header.Get("Authorization"),
			r.Header.Get("X-Amz-Date"), r.Header.Get("X-Amz-Content-Sha256"), string(b)}
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	bu, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	auth := api.NewAuthenticator(token)
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		api.S3Routes{Handler: storage.NewS3Proxy(bu)},
		api.WebUIRoutes{Handler: http.NotFoundHandler()},
	)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(s.Handler())
	t.Cleanup(front.Close)

	const sig = "AWS4-HMAC-SHA256 Credential=lydiabox/20261110/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=00ff"
	cases := []struct{ method, path, body string }{
		{"PUT", "/s3/workspace/a%20b%2Bc.txt?x-id=PutObject", "hello"},
		{"GET", "/s3?list-type=2&prefix=workspace%2F&delimiter=%2F", ""},
		{"DELETE", "/s3/home/agent/notes.md", ""},
	}
	for _, c := range cases {
		req, err := http.NewRequest(c.method, front.URL+c.path, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "box.example.com:8080"
		req.Header.Set("Authorization", sig)
		req.Header.Set("X-Amz-Date", "20261110T120000Z")
		req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"abc"` {
			t.Fatalf("%s %s: %d %v", c.method, c.path, resp.StatusCode, resp.Header)
		}
		g := <-got
		wantPath, wantQuery, _ := strings.Cut(c.path, "?")
		want := seen{c.method, wantPath, wantQuery, "box.example.com:8080", sig, "20261110T120000Z", "UNSIGNED-PAYLOAD", c.body}
		if g != want {
			t.Fatalf("backend saw %+v, want %+v", g, want)
		}
	}
}

func TestS3RouteDisabledGateway(t *testing.T) {
	g, err := storage.NewS3Gateway(storage.S3GatewayOptions{Config: disabledGatewayConfig(t), JuiceFSBinary: "/rt/juicefs", Runner: storage.OSRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/s3/workspace/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled gateway: %d", rec.Code)
	}
}

func disabledGatewayConfig(t *testing.T) *config.Config {
	cfg := config.Defaults(t.TempDir())
	cfg.S3.GatewayAddr = ""
	return cfg
}
