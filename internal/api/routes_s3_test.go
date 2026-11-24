package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/storage"
)

// TestS3RouteVerifiesAndResigns checks that /s3 reaches the gateway backend
// without API authentication, with the /s3 prefix removed, the Host header
// and query unchanged, and a new signature; and that unsigned requests and
// wrong signatures get an S3 XML error without reaching the backend.
func TestS3RouteVerifiesAndResigns(t *testing.T) {
	type seen struct {
		method, path, query, host, body string
		auth                            string
	}
	got := make(chan seen, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, string(b), r.Header.Get("Authorization")}
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	bu, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := storage.NewS3Proxy(bu, token)
	if err != nil {
		t.Fatal(err)
	}
	auth := api.NewAuthenticator(token)
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		api.S3Routes{Handler: proxy},
		api.WebUIRoutes{Handler: http.NotFoundHandler()},
	)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(s.Handler())
	t.Cleanup(front.Close)

	creds := aws.Credentials{AccessKeyID: storage.S3GatewayAccessKey, SecretAccessKey: token}
	sign := func(req *http.Request, c aws.Credentials) {
		req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
		err := v4.NewSigner().SignHTTP(context.Background(), c, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now(),
			func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
		if err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct{ method, path, body, wantPath string }{
		{"PUT", "/s3/lydiabox/workspace/a%20b%2Bc.txt?x-id=PutObject", "hello", "/lydiabox/workspace/a%20b%2Bc.txt"},
		{"GET", "/s3/lydiabox?delimiter=%2F&list-type=2&prefix=workspace%2F", "", "/lydiabox"},
		{"GET", "/s3/", "", "/"},
		{"DELETE", "/s3/lydiabox/home/agent/notes.md", "", "/lydiabox/home/agent/notes.md"},
	}
	host := strings.TrimPrefix(front.URL, "http://")
	for _, c := range cases {
		req, err := http.NewRequest(c.method, front.URL+c.path, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		sign(req, creds)
		clientAuth := req.Header.Get("Authorization")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"abc"` {
			t.Fatalf("%s %s: %d %v", c.method, c.path, resp.StatusCode, resp.Header)
		}
		g := <-got
		_, wantQuery, _ := strings.Cut(c.path, "?")
		if g.method != c.method || g.path != c.wantPath || g.query != wantQuery || g.host != host || g.body != c.body {
			t.Fatalf("backend saw %+v", g)
		}
		if g.auth == clientAuth || !strings.HasPrefix(g.auth, "AWS4-HMAC-SHA256 Credential=lydiabox/") {
			t.Fatalf("backend Authorization %q (client %q)", g.auth, clientAuth)
		}
	}

	for name, mutate := range map[string]func(*http.Request){
		"unsigned": func(*http.Request) {},
		"wrong secret": func(r *http.Request) {
			sign(r, aws.Credentials{AccessKeyID: "lydiabox", SecretAccessKey: "wrong-secret"})
		},
	} {
		req, _ := http.NewRequest("GET", front.URL+"/s3/lydiabox/k", nil)
		mutate(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Content-Type") != "application/xml" {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	select {
	case g := <-got:
		t.Fatalf("a rejected request reached the backend: %+v", g)
	default:
	}

	// /lydiabox is no longer an S3 route; it falls through to the web UI.
	resp, err := http.Get(front.URL + "/lydiabox/workspace/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/lydiabox: %d", resp.StatusCode)
	}
}

func TestS3RouteDisabledGateway(t *testing.T) {
	g, err := storage.NewS3Gateway(storage.S3GatewayOptions{Config: disabledGatewayConfig(t), JuiceFSBinary: "/rt/juicefs", Runner: storage.OSRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/s3/lydiabox/workspace/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled gateway: %d", rec.Code)
	}
}

func disabledGatewayConfig(t *testing.T) *config.Config {
	cfg := config.Defaults(t.TempDir())
	cfg.S3.GatewayAddr = ""
	return cfg
}
