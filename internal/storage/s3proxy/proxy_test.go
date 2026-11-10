package s3proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRoundTrip sends SDK-signed requests through the proxy to a fake
// backend that checks the re-signed request with the SDK signer.
func TestRoundTrip(t *testing.T) {
	type seen struct {
		method, path, query, host, body string
	}
	got := make(chan seen, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want, sig, err := backendCheck(r)
		if err != nil || want != sig {
			t.Errorf("backend signature check: want %s, got %s, err %v", want, sig, err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, string(b)}
		w.Header().Set("ETag", `"abc"`)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)
	front := httptest.NewServer(newTestProxy(t, backend.URL, time.Now()))
	t.Cleanup(front.Close)
	host := strings.TrimPrefix(front.URL, "http://")

	body := "round trip body"
	sum := sha256.Sum256([]byte(body))
	cases := []struct {
		name, method, path, body, hash, wantPath string
	}{
		{"list buckets", "GET", "/s3/", "", emptySHA256, "/"},
		{"list buckets without slash", "GET", "/s3", "", emptySHA256, "/"},
		{"list objects", "GET", "/s3/lydiabox?list-type=2&prefix=workspace%2F&delimiter=%2F", "", emptySHA256, "/lydiabox"},
		{"put", "PUT", "/s3/lydiabox/workspace/a%20b%2Bc.txt?x-id=PutObject", body, hex.EncodeToString(sum[:]), "/lydiabox/workspace/a%20b%2Bc.txt"},
		{"put unsigned", "PUT", "/s3/lydiabox/workspace/u.txt", body, unsignedPayload, "/lydiabox/workspace/u.txt"},
		{"delete", "DELETE", "/s3/lydiabox/home/agent/notes.md", "", emptySHA256, "/lydiabox/home/agent/notes.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, front.URL+c.path, strings.NewReader(c.body))
			signHeader(t, req, c.hash, testSecret, time.Now())
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"abc"` {
				t.Fatalf("status %d", resp.StatusCode)
			}
			g := <-got
			// The SDK signer sorts the query it signs and sends.
			want := seen{c.method, c.wantPath, req.URL.RawQuery, host, c.body}
			if g != want {
				t.Fatalf("backend saw %+v, want %+v", g, want)
			}
		})
	}

	t.Run("presigned", func(t *testing.T) {
		u := presign(t, "GET", front.URL+"/s3/lydiabox/workspace/a%20b.txt", 5*time.Minute, testSecret, time.Now())
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		g := <-got
		if g.path != "/lydiabox/workspace/a%20b.txt" || !strings.Contains(g.query, "X-Amz-Expires=300") {
			t.Fatalf("backend saw %+v", g)
		}
		if _, orig, _ := strings.Cut(u, "X-Amz-Signature="); strings.Contains(g.query, orig) {
			t.Fatal("the backend received the client signature")
		}
	})

	t.Run("wrong secret never reaches the backend", func(t *testing.T) {
		req, _ := http.NewRequest("GET", front.URL+"/s3/lydiabox/k", nil)
		signHeader(t, req, emptySHA256, "wrong-secret", time.Now())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 || resp.Header.Get("Content-Type") != "application/xml" ||
			!strings.Contains(string(b), "<Code>SignatureDoesNotMatch</Code>") {
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
		select {
		case g := <-got:
			t.Fatalf("backend saw %+v", g)
		default:
		}
	})
}

// TestResignMatchesBackend checks the re-signed Authorization header against
// the SDK signer for the rewritten path, without HTTP in between.
func TestResignMatchesBackend(t *testing.T) {
	p := newTestProxy(t, "http://backend", testTime)
	req, _ := http.NewRequest("PUT", "http://box:8080/s3/lydiabox/workspace/x%2Cy.txt?partNumber=1&uploadId=abc", strings.NewReader("x"))
	signHeader(t, req, unsignedPayload, testSecret, testTime)
	r := serverSide(t, req)
	v, serr := p.verify(r)
	if serr != nil {
		t.Fatal(serr)
	}
	rest, _ := p.strip(r.URL.Path)
	pl, ch := p.resign(r, v, rest)
	if ch != nil || pl.path != "/lydiabox/workspace/x,y.txt" || pl.rawPath != "/lydiabox/workspace/x%2Cy.txt" {
		t.Fatalf("plan %+v", pl)
	}
	out := r.Clone(r.Context())
	out.URL.Path, out.URL.RawPath = pl.path, pl.rawPath
	out.Header.Set("Authorization", pl.authorization)
	want, got, err := backendCheck(out)
	if err != nil || want != got {
		t.Fatalf("want %s, got %s, err %v", want, got, err)
	}
}
