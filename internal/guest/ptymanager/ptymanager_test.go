//go:build unix

package ptymanager

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRingWrapsAndKeepsNewest(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abc"))
	if got := string(r.Bytes()); got != "abc" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("defgh"))
	if got := string(r.Bytes()); got != "abcdefgh" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("ij"))
	if got := string(r.Bytes()); got != "cdefghij" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("0123456789"))
	if got := string(r.Bytes()); got != "23456789" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("x"))
	if got := string(r.Bytes()); got != "3456789x" {
		t.Fatalf("got %q", got)
	}
	if r.Total() != 21 || r.Len() != 8 {
		t.Fatalf("total %d len %d", r.Total(), r.Len())
	}
}

func TestRingDefaultIsOneMiB(t *testing.T) {
	r := NewRing(0)
	big := bytes.Repeat([]byte("a"), DefaultRingSize+10)
	big[len(big)-1] = 'z'
	r.Write(big)
	b := r.Bytes()
	if len(b) != 1<<20 || b[len(b)-1] != 'z' {
		t.Fatalf("len %d", len(b))
	}
}

func readUntil(t *testing.T, sub *Subscriber, want string) string {
	t.Helper()
	var buf bytes.Buffer
	deadline := time.After(10 * time.Second)
	for !strings.Contains(buf.String(), want) {
		select {
		case chunk, ok := <-sub.C():
			if !ok {
				t.Fatalf("subscriber closed before %q; got %q", want, buf.String())
			}
			buf.Write(chunk)
		case <-deadline:
			t.Fatalf("timeout waiting for %q; got %q", want, buf.String())
		}
	}
	return buf.String()
}

func newManager(t *testing.T) *Manager {
	t.Helper()
	m := New(Options{Logger: slog.New(slog.DiscardHandler), Shell: "/bin/sh", DefaultDir: t.TempDir(), Env: DefaultEnv("unix:///run/lydiabox/host-9001.sock")})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.Close(ctx)
	})
	return m
}

func TestMultiAttachReplayAndInput(t *testing.T) {
	m := newManager(t)
	s, err := m.Start(Spec{ID: "room", Command: []string{"/bin/sh"}, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Attach()
	if _, err := s.Write([]byte("echo first-$LYDIABOX_HOST_ADDR\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, a, "first-unix:///run/lydiabox/host-9001.sock")

	// A late client gets the replay, then live output like the first one.
	b, replay := s.Attach()
	if !strings.Contains(string(replay), "first-unix://") {
		t.Fatalf("replay missing earlier output: %q", replay)
	}
	if _, err := s.Write([]byte("echo second-$OPENAI_BASE_URL\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, a, "second-http://127.0.0.1:3425\r")
	readUntil(t, b, "second-http://127.0.0.1:3425\r")
	if info := s.Info(); info.Attached != 2 || !info.Running {
		t.Fatalf("info %+v", info)
	}
	s.Detach(b)
	if info := s.Info(); info.Attached != 1 {
		t.Fatalf("attached %d", info.Attached)
	}

	if err := s.Resize(132, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("stty size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, a, "40 132")

	if _, err := s.Write([]byte("exit 7\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("no exit")
	}
	if code, sig := s.Exit(); code != 7 || sig != "" {
		t.Fatalf("exit %d %q", code, sig)
	}
	if _, ok := <-a.C(); ok {
		// Drain whatever is left; the channel must end up closed.
		for range a.C() {
		}
	}
	if list := m.List(); len(list) != 1 || list[0].Running {
		t.Fatalf("list %+v", list)
	}
}

func TestSignalInterruptsForegroundProcess(t *testing.T) {
	m := newManager(t)
	s, err := m.Start(Spec{Command: []string{"/bin/sh", "-c", "echo ready; exec sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	sub, replay := s.Attach()
	if !strings.Contains(string(replay), "ready") {
		readUntil(t, sub, "ready")
	}
	if err := s.Signal("BOGUS"); err == nil {
		t.Fatal("expected error for unsupported signal")
	}
	if err := s.Signal("SIGINT"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGINT did not stop the process")
	}
	if code, sig := s.Exit(); sig != "SIGINT" || code != 130 {
		t.Fatalf("exit %d %q", code, sig)
	}
}

func TestKillForgetsSessionAndRejectsDuplicates(t *testing.T) {
	m := newManager(t)
	if _, err := m.Start(Spec{ID: "a", Command: []string{"sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(Spec{ID: "a", Command: []string{"sleep", "30"}}); err == nil {
		t.Fatal("duplicate running id accepted")
	}
	if _, err := m.Start(Spec{Command: []string{"/definitely/missing"}}); err == nil {
		t.Fatal("missing command accepted")
	}
	if err := m.Kill(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get("a"); err == nil {
		t.Fatal("killed session still listed")
	}
	if m.Running() != 0 {
		t.Fatalf("running %d", m.Running())
	}
}
