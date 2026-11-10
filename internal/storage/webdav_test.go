package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWebDAVProxyRewrites(t *testing.T) {
	type seen struct {
		path, dest, auth, cookie, method string
	}
	got := make(chan seen, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		got <- seen{path: r.URL.Path, dest: r.Header.Get("Destination"), auth: user + ":" + pass,
			cookie: r.Header.Get("Cookie"), method: r.Method}
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(207)
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:">`+
				`<D:response><D:href>/workspace/</D:href></D:response>`+
				`<D:response><D:href>http://127.0.0.1:9/workspace/a%20b.txt</D:href></D:response>`+
				`<response xmlns="DAV:"><href>/home/agent/</href></response></D:multistatus>`)
		case "MKCOL":
			w.Header().Set("Location", "/workspace/new/")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer backend.Close()
	proxy := NewWebDAVProxy(strings.TrimPrefix(backend.URL, "http://"), "internal", "s3cret")
	front := httptest.NewServer(proxy)
	defer front.Close()

	req, _ := http.NewRequest("PROPFIND", front.URL+"/webdav/workspace/", nil)
	req.Header.Set("Depth", "1")
	req.SetBasicAuth("lydiabox", "client-token")
	req.Header.Set("Cookie", "lydiabox_session=abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := <-got
	if s.path != "/workspace/" || s.auth != "internal:s3cret" || s.cookie != "" {
		t.Fatalf("backend saw %+v", s)
	}
	for _, want := range []string{"<D:href>/webdav/workspace/</D:href>", "<D:href>/webdav/workspace/a%20b.txt</D:href>",
		"<href>/webdav/home/agent/</href>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body lacks %s:\n%s", want, body)
		}
	}
	if resp.ContentLength != int64(len(body)) {
		t.Fatalf("Content-Length %d != %d", resp.ContentLength, len(body))
	}

	req, _ = http.NewRequest("MOVE", front.URL+"/webdav/workspace/a.txt", nil)
	req.Header.Set("Destination", front.URL+"/webdav/workspace/b.txt")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	s = <-got
	if s.method != "MOVE" || s.path != "/workspace/a.txt" || s.dest != backend.URL+"/workspace/b.txt" {
		t.Fatalf("MOVE: %+v", s)
	}

	req, _ = http.NewRequest("MKCOL", front.URL+"/webdav/workspace/new/", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	<-got
	if loc := resp.Header.Get("Location"); loc != "/webdav/workspace/new/" {
		t.Fatalf("Location = %q", loc)
	}
	if stripWebDAVPrefix("/webdav") != "/" || stripWebDAVPrefix("/webdav/x") != "/x" {
		t.Fatal("prefix stripping")
	}
}

func TestWebDAVSupervisor(t *testing.T) {
	cfg := testConfig(t)
	r := newFakeRunner(cfg)
	w, err := NewWebDAV(WebDAVOptions{Config: cfg, JuiceFSBinary: "/rt/juicefs", Runner: r, Logger: discardLogger(),
		Dial: func(ctx context.Context, addr string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	// 503 before start.
	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/webdav/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before start: %d", rec.Code)
	}
	if err := w.Start(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if w.Addr() == "" || !strings.HasPrefix(w.Addr(), "127.0.0.1:") {
		t.Fatalf("addr = %q", w.Addr())
	}
	r.mu.Lock()
	cmd := r.starts[0]
	r.mu.Unlock()
	if cmd.Args[0] != "webdav" || cmd.Args[2] != w.Addr() || !slices.Contains(cmd.Env, "WEBDAV_USER=lydiabox-internal") {
		t.Fatalf("command = %+v", cmd)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "WEBDAV_PASSWORD=") && len(e) < len("WEBDAV_PASSWORD=")+32 {
			t.Fatal("internal password is too short")
		}
	}

	// The backend is restarted after a crash.
	r.mu.Lock()
	first := r.procs[0]
	r.mu.Unlock()
	first.finish(nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.starts)
		r.mu.Unlock()
		if n == 2 && w.Addr() != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webdav backend was not restarted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := w.Stop(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	second := r.procs[1]
	r.mu.Unlock()
	second.mu.Lock()
	sigs := len(second.sigs)
	second.mu.Unlock()
	if sigs == 0 || w.Addr() != "" {
		t.Fatal("stop must terminate the backend")
	}
	if err := w.Start(context.Background(), "other"); err == nil {
		t.Fatal("unknown tenant must fail")
	}
}
