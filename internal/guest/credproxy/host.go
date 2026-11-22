package credproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultUpstream is the host magpie gateway.
const DefaultUpstream = "http://127.0.0.1:3425"

// credentialHeaders are removed from guest requests before InjectHeaders
// runs, so the guest's placeholder tokens never reach the upstream.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Proxy-Authorization"}

// HostGateway is the host side of the vsock port 9002 tunnel: an HTTP
// reverse proxy to the LLM gateway that adds the gateway credential. The
// guest listener on 127.0.0.1:3425 copies bytes to it unchanged, so this is
// where the requests of that listener are parsed: OpenAI-style paths
// without /v1 are forwarded under /v1 (normalizeOpenAIPath).
type HostGateway struct {
	upstream *url.URL
	inject   atomic.Pointer[func(http.Header)]
	proxy    *httputil.ReverseProxy
	// off holds the refusal text when the gateway is off (guest-native
	// mode): no request reaches the upstream.
	off atomic.Pointer[string]
}

// Disable turns the gateway off: every request gets 403 with reason, and
// nothing is sent to the upstream. Guest-native mode uses it, so the host
// routes no model traffic.
func (g *HostGateway) Disable(reason string) { g.off.Store(&reason) }

// NewHostGateway proxies to upstream (DefaultUpstream when empty).
func NewHostGateway(upstream string) (*HostGateway, error) {
	if upstream == "" {
		upstream = DefaultUpstream
	}
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("credproxy: invalid gateway upstream %q", upstream)
	}
	g := &HostGateway{upstream: u}
	g.proxy = &httputil.ReverseProxy{
		Rewrite:       g.rewrite,
		FlushInterval: -1,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 15 * time.Minute,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "lydiabox: LLM gateway unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
	return g, nil
}

// SetInjectHeaders installs the hook that adds the gateway credential
// (Phase 3 sets the magpie gateway key). A nil hook passes guest
// credentials through unchanged.
func (g *HostGateway) SetInjectHeaders(fn func(http.Header)) {
	if fn == nil {
		g.inject.Store(nil)
		return
	}
	g.inject.Store(&fn)
}

// Upstream returns the upstream URL.
func (g *HostGateway) Upstream() string { return g.upstream.String() }

func (g *HostGateway) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(g.upstream)
	if p, ok := normalizeOpenAIPath(pr.In.URL.Path); ok {
		pr.Out.URL.Path = strings.TrimSuffix(g.upstream.Path, "/") + p
		pr.Out.URL.RawPath = ""
	}
	pr.Out.Host = g.upstream.Host
	if fn := g.inject.Load(); fn != nil {
		for _, h := range credentialHeaders {
			pr.Out.Header.Del(h)
		}
		(*fn)(pr.Out.Header)
	}
}

// openAIRoots are the OpenAI and DeepSeek API paths that OpenAI-style SDKs
// append to a base URL. The guest base URLs are the gateway root
// (http://127.0.0.1:3425, design section 4.4.1), so these paths can arrive
// without the /v1 that the gateway serves them under.
var openAIRoots = []string{"/chat/completions", "/completions", "/responses", "/embeddings", "/models"}

// normalizeOpenAIPath returns "/v1"+p, true when p is an OpenAI-style API
// path without the /v1 prefix (for example /chat/completions or
// /models/<id>). Paths under /v1/ and other paths (Anthropic /v1/messages,
// Gemini /v1beta/..., magpie /api/...) stay unchanged.
func normalizeOpenAIPath(p string) (string, bool) {
	for _, root := range openAIRoots {
		if p == root || strings.HasPrefix(p, root+"/") {
			return "/v1" + p, true
		}
	}
	return p, false
}

// ServeHTTP implements http.Handler.
func (g *HostGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loopbackOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if off := g.off.Load(); off != nil {
		http.Error(w, "lydiabox: "+*off, http.StatusForbidden)
		return
	}
	g.proxy.ServeHTTP(w, r)
}

// Serve serves the gateway on ln (the guest port 9002 listener) until ctx
// is canceled.
func (g *HostGateway) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	stop := context.AfterFunc(ctx, func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	})
	defer stop()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
