package magpie_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/portwatcher/lydiabox/internal/magpie"
)

// fakeMagpieServer emulates the magpie gateway and admin web routes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) add(s string) { c.mu.Lock(); c.calls = append(c.calls, s); c.mu.Unlock() }
func (c *callLog) last() string { c.mu.Lock(); defer c.mu.Unlock(); return c.calls[len(c.calls)-1] }

func fakeMagpieServer(t *testing.T, webKey string) (*httptest.Server, *callLog) {
	t.Helper()
	calls := &callLog{}
	mux := http.NewServeMux()
	guard := func(h func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ok := false
			for _, c := range r.Cookies() {
				if strings.HasPrefix(c.Name, "magpie_web_") && c.Value == webKey {
					ok = true
				}
			}
			if !ok {
				http.Error(w, "sign in", http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			calls.add(r.Method + " " + r.URL.Path + " " + string(body))
			h(w, r, body)
		}
	}
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie","version":"0.1.3531"}`))
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[{"id":"openai/gpt-5.2","owned_by":"openai","context_window":400000},{"id":"group/budget","owned_by":"magpie"}]}`))
	})
	mux.HandleFunc("GET /api/providers", guard(func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Write([]byte(`{"providers":[{"id":"openai","name":"OpenAI","preset":"openai","chat":"https://api.openai.com/v1","off":false,"ready":true,
			"key":{"set":true,"masked":"sk-…abcd"},"models":[{"id":"gpt-5.2","name":""},{"id":"gpt-5.2-mini"}]}],"presets":[]}`))
	}))
	mux.HandleFunc("POST /api/provider/{action}", guard(func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.PathValue("action") {
		case "key":
			w.Write([]byte(`{"key":"sk-kept"}`))
		case "save", "on", "off", "delete":
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"Provider not found"}`))
		}
	}))
	mux.HandleFunc("GET /api/groups", guard(func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Write([]byte(`{"groups":[{"id":"budget","name":"Budget","members":["deepseek/deepseek-v4-pro"],"routing":"rotate","ready":true}]}`))
	}))
	mux.HandleFunc("POST /api/groups/{action}", guard(func(w http.ResponseWriter, r *http.Request, body []byte) { w.Write([]byte(`{}`)) }))
	mux.HandleFunc("GET /api/caller-keys", guard(func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Write([]byte(`{"keys":[{"id":"k1","name":"lydiabox-vm","masked":"lbx…"}]}`))
	}))
	mux.HandleFunc("POST /api/caller-keys/{action}", guard(func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in map[string]any
		json.Unmarshal(body, &in)
		w.Write([]byte(`{"keys":[{"id":"k1","name":"other"},{"id":"k2","name":"lydiabox-vm"}],"secret":"` + strOf(in["secret"]) + `"}`))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, calls
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func TestHTTPAdmin(t *testing.T) {
	srv, calls := fakeMagpieServer(t, "web-key-0123456789")
	addr := strings.TrimPrefix(srv.URL, "http://")
	a := magpie.NewHTTPAdmin(addr, addr, "web-key-0123456789")
	ctx := context.Background()

	h, err := a.Hello(ctx)
	if err != nil || h.Version != "0.1.3531" {
		t.Fatalf("Hello = %+v, %v", h, err)
	}
	ps, err := a.ListProviders(ctx)
	if err != nil || len(ps) != 1 || !ps[0].HasKey || strings.Join(ps[0].Models, ",") != "gpt-5.2,gpt-5.2-mini" {
		t.Fatalf("ListProviders = %+v, %v", ps, err)
	}
	ms, err := a.ListModels(ctx)
	if err != nil || len(ms) != 2 || ms[0].ContextWindow != 400000 {
		t.Fatalf("ListModels = %+v, %v", ms, err)
	}
	gs, err := a.ListGroups(ctx)
	if err != nil || len(gs) != 1 || gs[0].Routing != "rotate" {
		t.Fatalf("ListGroups = %+v, %v", gs, err)
	}
	name := "Renamed"
	if err := a.SaveProvider(ctx, magpie.ProviderInput{ID: "openai", Name: &name}); err != nil {
		t.Fatal(err)
	}
	last := calls.last()
	if !strings.Contains(last, "/api/provider/save") || !strings.Contains(last, `"key":"sk-kept"`) || !strings.Contains(last, `"name":"Renamed"`) {
		t.Fatalf("save body = %s", last)
	}
	k, err := a.AddGatewayKey(ctx, "lydiabox-vm", "lbx-gw-secret")
	if err != nil || k.ID != "k2" || k.Secret != "lbx-gw-secret" {
		t.Fatalf("AddGatewayKey = %+v, %v", k, err)
	}
	if err := a.DeleteGroups(ctx, []string{"budget"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetProviderOff(ctx, "openai", true); err != nil {
		t.Fatal(err)
	}
	err = a.DeleteProvider(ctx, "openai")
	if err != nil {
		t.Fatal(err)
	}
	err = (&magpie.HTTPAdmin{GatewayURL: srv.URL, WebURL: srv.URL, WebKey: "wrong"}).DeleteProvider(ctx, "x")
	if !errors.Is(err, magpie.ErrUnavailable) {
		t.Fatalf("bad web key = %v, want ErrUnavailable", err)
	}
	down := magpie.NewHTTPAdmin("127.0.0.1:1", "127.0.0.1:1", "k")
	if _, err := down.Hello(ctx); !errors.Is(err, magpie.ErrUnavailable) {
		t.Fatalf("unreachable magpie = %v", err)
	}
}
