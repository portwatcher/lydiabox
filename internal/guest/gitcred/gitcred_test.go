package gitcred

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

type fakeRunner struct {
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.name, f.args = name, args
	return nil, nil
}

func TestInstall(t *testing.T) {
	r := &fakeRunner{}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"install"}, Options{Runner: r, Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	want := "config --global --replace-all credential.helper /usr/local/bin/lydiabox-guest git-cred"
	if r.name != "git" || strings.Join(r.args, " ") != want {
		t.Fatalf("ran %s %v", r.name, r.args)
	}
}

func TestParseInput(t *testing.T) {
	req, err := ParseInput(strings.NewReader("protocol=https\r\nhost=github.com\npath=o/r.git\nusername=octo\nwwwauth[]=Basic\n\nhost=ignored\n"))
	if err != nil || req != (Request{Protocol: "https", Host: "github.com", Path: "o/r.git", Username: "octo"}) {
		t.Fatalf("%+v %v", req, err)
	}
}

func TestDialAddresses(t *testing.T) {
	ctx := context.Background()
	for _, addr := range []string{"", "tcp://1.2.3.4:5", "vsock://x:9001", "vsock://2:port", "unix://"} {
		if c, err := Dial(ctx, addr); err == nil {
			c.Close()
			t.Fatalf("Dial(%q) succeeded", addr)
		}
	}
	if _, err := Dial(ctx, ""); !errors.Is(err, ErrNoHostAddr) {
		t.Fatalf("empty: %v", err)
	}
}

func TestGetIgnoresOtherProtocolsAndBrokerMisses(t *testing.T) {
	var out bytes.Buffer
	dialed := false
	opts := Options{HostAddr: "unix:///nonexistent", Stdout: &out,
		Dial: func(context.Context, string) (net.Conn, error) { dialed = true; return nil, errors.New("no") }}
	opts.Stdin = strings.NewReader("protocol=ssh\nhost=github.com\n\n")
	if err := Run(context.Background(), []string{"get"}, opts); err != nil || dialed || out.Len() != 0 {
		t.Fatalf("ssh: %v dialed=%v out=%q", err, dialed, out.String())
	}
	opts.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	if err := Run(context.Background(), []string{"get"}, opts); err == nil || !dialed {
		t.Fatalf("dial failure must be reported: %v", err)
	}

	// A 404 from the broker means "no credential": print nothing, succeed.
	srv := newPipeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"not_found","message":"none"}}`, http.StatusNotFound)
	}))
	opts.Dial = srv.dial
	opts.Stdin = strings.NewReader("protocol=https\nhost=gitlab.com\n\n")
	if err := Run(context.Background(), []string{"get"}, opts); err != nil || out.Len() != 0 {
		t.Fatalf("404: %v %q", err, out.String())
	}
	for _, action := range []string{"store", "erase"} {
		opts.Stdin = strings.NewReader("protocol=https\nhost=github.com\npassword=x\n\n")
		if err := Run(context.Background(), []string{action}, opts); err != nil || out.Len() != 0 {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if err := Run(context.Background(), []string{"bogus"}, opts); err == nil {
		t.Fatal("unknown action must fail")
	}
}

// pipeServer serves an http.Handler over in-memory connections.
type pipeServer struct{ ln *pipeListener }

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { close(l.done); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "unix"} }

func newPipeServer(t *testing.T, h http.Handler) *pipeServer {
	ln := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &pipeServer{ln: ln}
}

func (p *pipeServer) dial(ctx context.Context, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case p.ln.conns <- b:
		return a, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
