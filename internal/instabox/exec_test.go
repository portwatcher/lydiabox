package instabox_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

func TestExec(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "exec")
	res, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "echo hello world", Cwd: "/data/workspace", TimeoutSeconds: 30})
	if err != nil || res.Stdout != "hello world\n" || res.ExitCode != 0 {
		t.Fatalf("exec: %+v %v", res, err)
	}
	res, err = r.client.Exec(ctx, m.ID, instabox.ExecRequest{Argv: []string{"exit", "3"}})
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("non-zero exit is no error: %+v %v", res, err)
	}
	res, err = r.client.Exec(ctx, m.ID, instabox.ExecRequest{Argv: []string{"cat"}, Stdin: "piped"})
	if err != nil || res.Stdout != "piped" {
		t.Fatalf("stdin: %+v %v", res, err)
	}
	for _, bad := range []instabox.ExecRequest{
		{}, {Cmd: "x", Argv: []string{"y"}}, {Argv: []string{""}}, {Cmd: "x", TimeoutSeconds: 601},
		{Cmd: "x", Stdin: strings.Repeat("a", 1<<20+1)},
	} {
		if _, err := r.client.Exec(ctx, m.ID, bad); err == nil {
			t.Fatalf("invalid request %+v accepted", bad)
		}
	}
	if _, err := r.client.Exec(ctx, "m_aaaaaaaaaaaaaaaaaaaaaaaaaa", instabox.ExecRequest{Cmd: "true"}); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("exec on unknown machine: %v", err)
	}
}

func TestExecKeepalive(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "keepalive")
	res, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "echo slow"})
	if err != nil || res.Stdout != "slow\n" {
		t.Fatalf("keepalive exec: %+v %v", res, err)
	}
	reqs := r.fake.Requests()
	if reqs[len(reqs)-1].Header.Get("X-Instabox-Exec-Keepalive") != "1" {
		t.Fatal("no keepalive header")
	}
	r.fake.FailNextExecAfterStart(instabox.CodeHostUnavailable, http.StatusServiceUnavailable)
	_, err = r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "echo slow"})
	var apiErr *instabox.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != instabox.CodeHostUnavailable {
		t.Fatalf("late error: %v", err)
	}
}

func TestExecRetriesOnlyWhenNotStarted(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "execretry")
	path := "/v1/machines/" + m.ID + "/exec"

	// A concurrency limit proves the command did not start: retried.
	r.fake.InjectError(http.MethodPost, path, http.StatusTooManyRequests, instabox.CodeQuotaExceeded, true, 0, 1)
	if _, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "echo a"}); err != nil {
		t.Fatalf("exec after 429: %v", err)
	}
	// host_unavailable may come after the command ran: not retried.
	r.fake.InjectError(http.MethodPost, path, http.StatusServiceUnavailable, instabox.CodeHostUnavailable, true, 0, 1)
	if _, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "echo a"}); !errors.Is(err, instabox.ErrUnavailable) {
		t.Fatalf("exec after 503: %v", err)
	}
	// A dropped connection is not retried either.
	before := len(r.fake.Requests())
	r.fake.InjectDisconnect(http.MethodPost, path, true, 1)
	if _, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "echo a"}); err == nil {
		t.Fatal("exec over a dropped connection succeeded")
	}
	if n := len(r.fake.Requests()) - before; n != 1 {
		t.Fatalf("exec sent %d times after a dropped connection", n)
	}
	for _, req := range r.fake.Requests() {
		if req.Path == path && req.Header.Get("Idempotency-Key") != "" {
			t.Fatal("exec sent an Idempotency-Key")
		}
	}
}

func TestExecStream(t *testing.T) {
	r := newRig(t, instaboxtest.Options{Exec: func(_ string, req instaboxtest.ExecRequest) instaboxtest.ExecResult {
		return instaboxtest.ExecResult{Stdout: "line 1\nline 2\n", Stderr: "warn\n", ExitCode: 7}
	}})
	ctx := ctxT(t)
	m := r.running(t, "stream")
	var stdout, stderr bytes.Buffer
	res, err := r.client.ExecStream(ctx, m.ID, instabox.ExecRequest{Cmd: "make"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "line 1\nline 2\n" || stderr.String() != "warn\n" || res.ExitCode != 7 || res.Stdout != "" {
		t.Fatalf("stream: out=%q err=%q res=%+v", stdout.String(), stderr.String(), res)
	}
	reqs := r.fake.Requests()
	if got := reqs[len(reqs)-1].Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
}

// sseServer answers exec with a fixed event stream.
func sseServer(t *testing.T, body string) *instabox.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := instabox.NewClient(instabox.Options{BaseURL: srv.URL, APIKey: "ibk_x"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExecStreamEvents(t *testing.T) {
	ctx := ctxT(t)
	c := sseServer(t, ": ping\n\nevent: stdout\ndata: {\"data\":\"a\"}\n\n"+
		"event: error\ndata: {\"error\":{\"code\":\"host_unavailable\",\"message\":\"lost\",\"requestId\":\"r\",\"retryable\":false,\"operationId\":null}}\n\n")
	var out bytes.Buffer
	_, err := c.ExecStream(ctx, "m", instabox.ExecRequest{Cmd: "x"}, &out, nil)
	if instabox.CodeOf(err) != instabox.CodeHostUnavailable || out.String() != "a" {
		t.Fatalf("error event: %v out=%q", err, out.String())
	}
	c = sseServer(t, "event: stdout\ndata: {\"data\":\"a\"}\n\n")
	if _, err := c.ExecStream(ctx, "m", instabox.ExecRequest{Cmd: "x"}, nil, nil); err == nil || !strings.Contains(err.Error(), "without an exit event") {
		t.Fatalf("cut stream: %v", err)
	}
	c = sseServer(t, "event: exit\ndata: {\"stdout\":\"\",\n"+"data: \"stderr\":\"\",\"exitCode\":2,\"timedOut\":true,\"truncated\":false}\n\n")
	res, err := c.ExecStream(ctx, "m", instabox.ExecRequest{Cmd: "x"}, nil, nil)
	if err != nil || res.ExitCode != 2 || !res.TimedOut {
		t.Fatalf("multi-line data: %+v %v", res, err)
	}
}

func TestFiles(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "files")
	w, err := r.client.PutFileBytes(ctx, m.ID, "/data/.lydiabox/guest.env", []byte("A=1\n"), instabox.PutFileOptions{Mode: 0o600, Parents: true})
	if err != nil || !w.Created || w.Mode != "0600" || w.Size != 4 || len(w.SHA256) != 64 {
		t.Fatalf("put: %+v %v", w, err)
	}
	w, err = r.client.PutFileBytes(ctx, m.ID, "/data/.lydiabox/guest.env", []byte("A=2\n"), instabox.PutFileOptions{})
	if err != nil || w.Created || w.Mode != "0600" {
		t.Fatalf("replace keeps mode: %+v %v", w, err)
	}
	data, err := r.client.ReadFile(ctx, m.ID, "/data/.lydiabox/guest.env", 0)
	if err != nil || string(data) != "A=2\n" {
		t.Fatalf("read: %q %v", data, err)
	}
	if _, err := r.client.ReadFile(ctx, m.ID, "/data/.lydiabox/guest.env", 2); err == nil {
		t.Fatal("over-limit read accepted")
	}
	f, err := r.client.GetFile(ctx, m.ID, "/data/.lydiabox/guest.env")
	if err != nil || f.Mode != "0600" || f.Size != 4 || f.ETag == "" {
		t.Fatalf("get: %+v %v", f, err)
	}
	f.Close()
	fi, err := r.client.StatFile(ctx, m.ID, "/data/.lydiabox")
	if err != nil || fi.Type != "dir" || len(fi.Entries) != 1 || fi.Entries[0].Name != "guest.env" {
		t.Fatalf("stat dir: %+v %v", fi, err)
	}
	fi, err = r.client.StatFile(ctx, m.ID, "/data/.lydiabox/guest.env")
	if err != nil || fi.Content != "A=2\n" {
		t.Fatalf("stat file: %+v %v", fi, err)
	}

	if _, err := r.client.PutFileBytes(ctx, m.ID, "/data/no/such/x", nil, instabox.PutFileOptions{}); !errors.Is(err, instabox.ErrNotFound) || instabox.CodeOf(err) != instabox.CodeMissingParent {
		t.Fatalf("missing parent: %v", err)
	}
	if _, err := r.client.PutFileBytes(ctx, m.ID, "relative", nil, instabox.PutFileOptions{}); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := r.client.GetFile(ctx, m.ID, "/data/missing"); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := r.client.GetFile(ctx, m.ID, "/data/home"); instabox.CodeOf(err) != instabox.CodeNotAFile {
		t.Fatalf("directory download: %v", err)
	}
}

func TestPutFileRetriesOnlySeekableBodies(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "upload")
	path := "/v1/machines/" + m.ID + "/files"
	r.fake.InjectError(http.MethodPut, path, http.StatusServiceUnavailable, instabox.CodeHostUnavailable, true, 0, 1)
	if _, err := r.client.PutFile(ctx, m.ID, "/data/a", strings.NewReader("seekable"), instabox.PutFileOptions{}); err != nil {
		t.Fatalf("seekable upload: %v", err)
	}
	if got, _, _ := r.fake.File(m.ID, "/data/a"); string(got) != "seekable" {
		t.Fatalf("file = %q", got)
	}
	r.fake.InjectError(http.MethodPut, path, http.StatusServiceUnavailable, instabox.CodeHostUnavailable, true, 0, 1)
	pr, pw := io.Pipe()
	go func() {
		fmt.Fprint(pw, "stream")
		pw.Close()
	}()
	if _, err := r.client.PutFile(ctx, m.ID, "/data/b", pr, instabox.PutFileOptions{}); !errors.Is(err, instabox.ErrUnavailable) {
		t.Fatalf("pipe upload was retried or succeeded: %v", err)
	}
}
