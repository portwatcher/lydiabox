package magpie_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/magpie"
)

// signInServer answers the sign-in routes with bodies recorded from magpie
// v0.1.3531 (fake accounts, no network).
func signInServer(t *testing.T) (*httptest.Server, *callLog) {
	t.Helper()
	calls := &callLog{}
	mux := http.NewServeMux()
	h := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			calls.add(r.Method + " " + r.URL.EscapedPath() + " " + string(b))
			if r.Header.Get("Cookie") != "magpie_web_"+portOf(r.Host)+"=web-key" {
				http.Error(w, "sign in", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(status)
			w.Write([]byte(body))
		}
	}
	mux.HandleFunc("GET /api/providers", h(200, `{"providers":[{"id":"claude","name":"Claude Code","anthropic":"https://api.anthropic.com",
		"key":{"set":false,"optional":true},"ready":true,"models":[],"off":false,
		"account":{"user":"a@example.com","agent":"claude","agentName":"Claude Code","logins":[
			{"agent":"claude","user":"a@example.com","seen":"2026-11-14T11:45:11Z","active":true,"on":true},
			{"agent":"claude","user":"b@example.com","plan":"max","lapsed":"refresh token revoked"},
			{"agent":"claude","user":"c@example.com","on":true,"paused":true,"lapsed":false}]}},
		{"id":"codex","name":"Codex","responses":"https://chatgpt.com/backend-api/codex","ready":true,"models":["gpt-5-codex"],
		"account":{"user":"me@example.com","agent":"codex"}},
		{"id":"openai","name":"OpenAI","preset":"openai","key":{"set":true},"models":[]}],"presets":[]}`))
	mux.HandleFunc("POST /api/signin", h(200, `{"id":"YlNHut3ORIVR","agent":"codex","url":"https://auth.openai.com/codex/device","state":"waiting","code":"ABCD-1234"}`))
	mux.HandleFunc("GET /api/signin/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "YlNHut3ORIVR" {
			calls.add("GET " + r.URL.EscapedPath())
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		h(200, `{"id":"YlNHut3ORIVR","agent":"claude","url":"","state":"failed","error":"installing Claude Code failed"}`)(w, r)
	})
	mux.HandleFunc("POST /api/signin/{id}/callback", h(400, `{"error":"this sign-in can't be finished from a pasted address"}`))
	mux.HandleFunc("POST /api/signin/{id}/cancel", h(204, ``))
	mux.HandleFunc("POST /api/signin/claude-token", h(200, `{"user":"Claude setup-token ef41f8"}`))
	mux.HandleFunc("POST /api/login/forget", h(400, `{"agent":"claude","code":"signed_in_only","error":"Claude Code is signed in to a@example.com, the only account in magpie it can be signed in to","user":"a@example.com"}`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, calls
}

func portOf(host string) string {
	_, port, _ := strings.Cut(host, ":")
	return port
}

func TestHTTPAdminSignIn(t *testing.T) {
	srv, calls := signInServer(t)
	addr := strings.TrimPrefix(srv.URL, "http://")
	a := magpie.NewHTTPAdmin(addr, addr, "web-key")
	ctx := context.Background()

	ps, err := a.ListProviders(ctx)
	if err != nil || len(ps) != 3 {
		t.Fatalf("ListProviders = %+v, %v", ps, err)
	}
	acc := ps[0].Account
	if acc == nil || acc.Agent != "claude" || len(acc.Logins) != 3 {
		t.Fatalf("claude account = %+v", acc)
	}
	if l := acc.Logins[0]; l.User != "a@example.com" || !l.Active || !l.On || l.Lapsed {
		t.Errorf("login 0 = %+v", l)
	}
	if l := acc.Logins[1]; l.Plan != "max" || !l.Lapsed || l.On {
		t.Errorf("login 1 = %+v", l)
	}
	if l := acc.Logins[2]; !l.Paused || l.Lapsed {
		t.Errorf("login 2 = %+v", l)
	}
	if c := ps[1].Account; c == nil || len(c.Logins) != 1 || c.Logins[0].User != "me@example.com" || !c.Logins[0].Active {
		t.Errorf("an account without logins lists its user: %+v", c)
	}
	if ps[2].Account != nil {
		t.Errorf("an API key provider has no account: %+v", ps[2].Account)
	}

	si, err := a.StartSignIn(ctx, "codex", "device")
	if err != nil || si.ID != "YlNHut3ORIVR" || si.Code != "ABCD-1234" || si.State != magpie.SignInWaiting {
		t.Fatalf("StartSignIn = %+v, %v", si, err)
	}
	if last := calls.last(); last != `POST /api/signin {"agent":"codex","site":"device"}` {
		t.Errorf("start body = %s", last)
	}
	if _, err := a.StartSignIn(ctx, "claude", ""); err != nil || calls.last() != `POST /api/signin {"agent":"claude"}` {
		t.Errorf("claude start = %s, %v", calls.last(), err)
	}
	st, err := a.SignInStatus(ctx, "YlNHut3ORIVR")
	if err != nil || st.State != magpie.SignInFailed || st.Error == "" {
		t.Fatalf("SignInStatus = %+v, %v", st, err)
	}
	if _, err := a.SignInStatus(ctx, "a/b"); !errors.Is(err, magpie.ErrNotFound) || !strings.Contains(calls.last(), "/api/signin/a%2Fb") {
		t.Errorf("escaped status = %s, %v", calls.last(), err)
	}
	err = a.SubmitSignInCode(ctx, "YlNHut3ORIVR", "code#state")
	if !errors.Is(err, magpie.ErrBadRequest) || calls.last() != `POST /api/signin/YlNHut3ORIVR/callback {"url":"code#state"}` {
		t.Errorf("SubmitSignInCode = %s, %v", calls.last(), err)
	}
	if err := a.CancelSignIn(ctx, "YlNHut3ORIVR"); err != nil {
		t.Errorf("CancelSignIn = %v", err)
	}
	user, err := a.AddSetupToken(ctx, "sk-ant-oat01-x")
	if err != nil || user != "Claude setup-token ef41f8" || calls.last() != `POST /api/signin/claude-token {"token":"sk-ant-oat01-x"}` {
		t.Errorf("AddSetupToken = %q %s, %v", user, calls.last(), err)
	}
	err = a.ForgetLogin(ctx, "claude", "a@example.com")
	if !errors.Is(err, magpie.ErrConflict) || !strings.Contains(err.Error(), "only account") {
		t.Errorf("ForgetLogin of the held account = %v, want ErrConflict", err)
	}
	if last := calls.last(); last != `POST /api/login/forget {"agent":"claude","user":"a@example.com"}` {
		t.Errorf("forget body = %s", last)
	}
}
