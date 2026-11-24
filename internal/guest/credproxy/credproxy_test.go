package credproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestGatewayRoundTripOverUnixSockets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: path=%s auth=%q key=%q xkey=%q\n\n", r.URL.Path, r.Header.Get("Authorization"),
			r.Header.Get("X-Lydiabox-Gateway"), r.Header.Get("X-Api-Key"))
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	gw, err := NewHostGateway(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetInjectHeaders(func(h http.Header) {
		h.Set("Authorization", "Bearer gateway-key")
		h.Set("X-Lydiabox-Gateway", "vm")
	})
	dir := shortDir(t)
	hostSock := filepath.Join(dir, "host-9002.sock")
	hostLn, err := rpc.Listen("unix://" + hostSock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.Serve(ctx, hostLn)

	guestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ServeTunnel(ctx, guestLn, AddrDialer("unix://"+hostSock), nil) }()

	req, _ := http.NewRequest(http.MethodPost, "http://"+guestLn.Addr().String()+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer lydiabox-local")
	req.Header.Set("X-Api-Key", "lydiabox-local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want := `data: path=/v1/messages auth="Bearer gateway-key" key="vm" xkey=""`
	if !strings.Contains(string(body), want) || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("body %q", body)
	}

	req, _ = http.NewRequest(http.MethodPost, "http://"+guestLn.Addr().String()+"/v1/messages", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel did not stop")
	}
}

func TestConnectorMCPProxyForwardsOnlyMCP(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "host-9001.sock")
	ln, err := rpc.Listen("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s %s", r.Method, r.URL.Path, b)
	})}
	go broker.Serve(ln)
	defer broker.Close()

	proxy := httptest.NewServer(ConnectorMCPProxy(AddrDialer("unix://" + sock)))
	defer proxy.Close()
	resp, err := http.Post(proxy.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `POST /mcp {"jsonrpc":"2.0"}` {
		t.Fatalf("got %q", b)
	}
	resp, err = http.Get(proxy.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSpliceHalfClose(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	go Splice(context.Background(), a2, b1)
	go func() {
		r := bufio.NewReader(b2)
		line, _ := r.ReadString('\n')
		_, _ = b2.Write([]byte("echo " + line))
		b2.Close()
	}()
	_, _ = a1.Write([]byte("hello\n"))
	out, _ := io.ReadAll(a1)
	if string(out) != "echo hello\n" {
		t.Fatalf("got %q", out)
	}
}

func TestSubAddr(t *testing.T) {
	if got, _ := rpc.SubAddr(rpc.HostAddrVsock, rpc.PortHostGateway); got != rpc.GatewayAddrVsock {
		t.Fatalf("vsock %s", got)
	}
	if got, _ := rpc.SubAddr(rpc.HostAddrDocker, rpc.PortHostGateway); got != rpc.GatewayAddrDocker {
		t.Fatalf("unix %s", got)
	}
}

func TestNormalizeOpenAIPath(t *testing.T) {
	cases := map[string]string{
		"/chat/completions":    "/v1/chat/completions",
		"/completions":         "/v1/completions",
		"/responses":           "/v1/responses",
		"/responses/resp_1":    "/v1/responses/resp_1",
		"/embeddings":          "/v1/embeddings",
		"/models":              "/v1/models",
		"/models/kimi/kimi-k3": "/v1/models/kimi/kimi-k3",
		// Already under /v1, or not an OpenAI-style path: unchanged.
		"/v1/chat/completions": "/v1/chat/completions",
		"/v1/messages":         "/v1/messages",
		"/v1/models":           "/v1/models",
		"/v1beta/models/gemini-3:generateContent": "/v1beta/models/gemini-3:generateContent",
		"/api/hello":         "/api/hello",
		"/chat/completionsx": "/chat/completionsx",
		"/":                  "/",
	}
	for in, want := range cases {
		got, changed := normalizeOpenAIPath(in)
		if got != want || changed != (in != want) {
			t.Errorf("normalizeOpenAIPath(%q) = %q, %v; want %q", in, got, changed, want)
		}
	}
}

// TestGatewayAddsV1ForOpenAIPaths sends requests through the host gateway:
// OpenAI-style paths without /v1 reach the upstream under /v1, and paths
// that already have /v1 pass through unchanged, with the query.
func TestGatewayAddsV1ForOpenAIPaths(t *testing.T) {
	seen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.RequestURI()
	}))
	defer upstream.Close()
	gw, err := NewHostGateway(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(gw)
	defer front.Close()
	for in, want := range map[string]string{
		"/chat/completions":       "/v1/chat/completions",
		"/models?limit=5":         "/v1/models?limit=5",
		"/v1/chat/completions":    "/v1/chat/completions",
		"/v1/messages?beta=true":  "/v1/messages?beta=true",
		"/v1beta/models/g:stream": "/v1beta/models/g:stream",
	} {
		resp, err := http.Post(front.URL+in, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := <-seen; got != want {
			t.Errorf("%s reached the upstream as %s, want %s", in, got, want)
		}
	}
}

// In guest-native mode the host routes no model traffic: a request with a
// subscription token gets 403 and never reaches the upstream.
func TestDisabledGatewaySendsNothingUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the upstream got %s %s", r.Method, r.URL)
	}))
	defer upstream.Close()
	gw, err := NewHostGateway(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	gw.Disable("the model gateway is off")
	front := httptest.NewServer(gw)
	defer front.Close()
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-subscription")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
}
