//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

// startLogged runs a cell like start, with its log in buf.
func (r *cellRig) startLogged(buf io.Writer) *cell {
	t := r.t
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{HomeDir: r.root, ConfigFile: os.DevNull, Environ: func() []string { return r.env }})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSecrets(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &cell{cancel: cancel, done: make(chan error, 1), out: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	go func() { c.done <- serve(ctx, Env{Stdout: io.Discard, Stderr: io.Discard}, cfg, log, true) }()
	t.Cleanup(func() {
		if !c.stopped {
			c.stop(t)
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _ := r.call(http.MethodGet, "/api/v1/ready", ""); code == http.StatusOK {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatal("the cell did not get ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var shellPID = regexp.MustCompile(`PID=(\d+)X`)

// terminalPID asks the shell of ws for its PID.
func terminalPID(ctx context.Context, t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("echo PID=$$\"X\"\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		if m := shellPID.FindStringSubmatch(out.String()); m != nil {
			return m[1]
		}
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("terminal output %q: %v", out.String(), err)
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		}
	}
}

// liveShell starts a shell session, attaches its terminal, and returns the
// session ID, the socket, and the shell PID.
func liveShell(ctx context.Context, t *testing.T, r *cellRig) (string, *websocket.Conn, string) {
	t.Helper()
	b := r.must(http.MethodPost, "/api/v1/sessions", `{"harness":"shell","workspaceDir":"/workspace"}`, http.StatusCreated)
	var sess struct{ ID string }
	if err := json.Unmarshal(b, &sess); err != nil {
		t.Fatal(err)
	}
	ws := r.dialWS(ctx, "/api/v1/sessions/"+sess.ID+"/ws", api.PTYSubprotocol)
	return sess.ID, ws, terminalPID(ctx, t, ws)
}

// hibernate pauses the machine and returns the last control frame of the
// open terminal.
func hibernate(ctx context.Context, r *cellRig, ws *websocket.Conn) string {
	r.must(http.MethodPost, "/api/v1/vm/hibernate", "", http.StatusOK)
	last := ""
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return last
		}
		if typ == websocket.MessageText {
			last = string(data)
		}
	}
}

// TestInstaboxResumeAfterHibernateWithATerminal hibernates the machine
// while a terminal is open and resumes it: the resume is as fast as other
// wakes, the log says what the driver asked for and how long it took, and
// the terminal attaches again to the same shell.
func TestInstaboxResumeAfterHibernateWithATerminal(t *testing.T) {
	checkGoroutines(t)
	r := newCellRig(t)
	var logs lockedBuffer
	c := r.startLogged(&logs)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id, ws, pid := liveShell(ctx, t, r)
	for round := 0; round < 2; round++ {
		if last := hibernate(ctx, r, ws); !strings.Contains(last, `"paused"`) {
			t.Fatalf("round %d: last terminal frame %s", round, last)
		}
		start := time.Now()
		r.must(http.MethodPost, "/api/v1/vm/wake", "", http.StatusOK)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("round %d: the resume took %s\n%s", round, d, logs.String())
		}
		ws = r.dialWS(ctx, "/api/v1/sessions/"+id+"/ws", api.PTYSubprotocol)
		if got := terminalPID(ctx, t, ws); got != pid {
			t.Fatalf("round %d: shell PID %s after the resume, want %s", round, got, pid)
		}
	}
	if n := strings.Count(logs.String(), `msg="instabox: machine running" component=instabox tenant=alice action=resume took=`); n != 2 {
		t.Errorf("%d resume lines, want 2:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), `msg="guest link up" component=instabox tenant=alice connect=`) {
		t.Errorf("the link-up line has no connect time:\n%s", logs.String())
	}
	ws.Close(websocket.StatusNormalClosure, "")
	r.must(http.MethodDelete, "/api/v1/sessions/"+id, "", http.StatusNoContent)
	c.stop(t)
}

// A slow InstaBox resume is visible in the log with its duration, and the
// wake waits for it.
func TestInstaboxSlowResumeIsLogged(t *testing.T) {
	checkGoroutines(t)
	r := newCellRigWith(t, func(o *instaboxtest.Options) { o.ResumeDelay = 3 * time.Second })
	var logs lockedBuffer
	c := r.startLogged(&logs)
	r.must(http.MethodPost, "/api/v1/fs/workspace/mkdir", `{"path":"x","parents":true}`, http.StatusCreated)
	r.must(http.MethodPost, "/api/v1/vm/hibernate", "", http.StatusOK)
	start := time.Now()
	r.must(http.MethodPost, "/api/v1/vm/wake", "", http.StatusOK)
	if d := time.Since(start); d < 3*time.Second || d > 15*time.Second {
		t.Fatalf("wake took %s", d)
	}
	m := regexp.MustCompile(`msg="instabox: machine running" component=instabox tenant=alice action=resume took=(\S+)`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no resume line:\n%s", logs.String())
	}
	if d, err := time.ParseDuration(m[1]); err != nil || d < 3*time.Second {
		t.Fatalf("resume took=%s (%v), want at least 3s", m[1], err)
	}
	c.stop(t)
}

// A resume that boots the machine fresh (InstaBox could not restore the
// processes) is a warning, and the terminal of before ends cleanly: the
// session is exited, and its socket gets a conflict, not a wait.
func TestInstaboxFreshResumeEndsTheTerminal(t *testing.T) {
	checkGoroutines(t)
	r := newCellRig(t)
	var logs lockedBuffer
	c := r.startLogged(&logs)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id, ws, _ := liveShell(ctx, t, r)
	if !r.fake.FreshResumeNext(r.machine()) {
		t.Fatal("fresh resume")
	}
	if last := hibernate(ctx, r, ws); !strings.Contains(last, `"paused"`) {
		t.Fatalf("last terminal frame %s", last)
	}
	r.must(http.MethodPost, "/api/v1/vm/wake", "", http.StatusOK)
	if !strings.Contains(logs.String(), `level=WARN msg="instabox: the resume did not keep the processes; the machine booted fresh"`) {
		t.Fatalf("no warning for the fresh boot:\n%s", logs.String())
	}
	ws = r.dialWS(ctx, "/api/v1/sessions/"+id+"/ws", api.PTYSubprotocol)
	_, data, err := ws.Read(ctx)
	if err != nil || !strings.Contains(string(data), `"conflict"`) || !strings.Contains(string(data), "ended while the VM was paused") {
		t.Fatalf("socket after the fresh boot: %s %v", data, err)
	}
	ws.CloseNow()
	var list struct{ Sessions []struct{ ID, Status string } }
	if err := json.Unmarshal(r.must(http.MethodGet, "/api/v1/sessions", "", http.StatusOK), &list); err != nil {
		t.Fatal(err)
	}
	status := ""
	for _, s := range list.Sessions {
		if s.ID == id {
			status = s.Status
		}
	}
	if status != "exited" {
		t.Fatalf("session status %q, want exited", status)
	}
	c.stop(t)
}
