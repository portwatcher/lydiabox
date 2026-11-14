package magpie

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Sign-in states magpie reports for a subscription sign-in.
const (
	SignInStarting   = "starting"
	SignInInstalling = "installing"
	SignInWaiting    = "waiting"
	SignInDone       = "done"
	SignInFailed     = "failed"
	SignInCanceled   = "canceled"
)

// SignIn is a subscription sign-in under way in magpie (POST /api/signin).
// It never carries a token: magpie keeps the tokens it receives in its own
// data directory on the host.
type SignIn struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	State string `json:"state"`
	// URL is the vendor page the user opens to sign in.
	URL string `json:"url"`
	// Code is the device code the user enters on the vendor page.
	Code string `json:"code"`
	// PasteCode and PasteCallback ask for the code or the address the
	// vendor page shows at the end, sent back with SubmitSignInCode.
	PasteCode     bool `json:"pasteCode"`
	PasteCallback bool `json:"pasteCallback"`
	// Installing names the vendor CLI magpie installs before the sign-in.
	Installing string `json:"installing"`
	User       string `json:"user"`
	Error      string `json:"error"`
}

// RemoteAccount is the subscription a magpie provider serves.
type RemoteAccount struct {
	Agent  string
	Logins []RemoteLogin
}

// RemoteLogin is one signed-in subscription account.
type RemoteLogin struct {
	User   string
	Plan   string
	Active bool
	On     bool
	Paused bool
	// Lapsed is set when the vendor refused the sign-in.
	Lapsed bool
}

type accountWire struct {
	Agent  string `json:"agent"`
	User   string `json:"user"`
	Logins []struct {
		User   string          `json:"user"`
		Plan   string          `json:"plan"`
		Active bool            `json:"active"`
		On     bool            `json:"on"`
		Paused bool            `json:"paused"`
		Lapsed json.RawMessage `json:"lapsed"`
	} `json:"logins"`
}

func (w *accountWire) remote() *RemoteAccount {
	if w == nil || w.Agent == "" {
		return nil
	}
	out := &RemoteAccount{Agent: w.Agent, Logins: []RemoteLogin{}}
	for _, l := range w.Logins {
		out.Logins = append(out.Logins, RemoteLogin{User: l.User, Plan: l.Plan, Active: l.Active, On: l.On,
			Paused: l.Paused, Lapsed: truthy(l.Lapsed)})
	}
	if len(out.Logins) == 0 && w.User != "" {
		out.Logins = append(out.Logins, RemoteLogin{User: w.User, Active: true, On: true})
	}
	return out
}

// truthy reads a JSON value that magpie sends as a bool or a reason string.
func truthy(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	switch string(v) {
	case "", "null", "false", `""`, "0":
		return false
	}
	return true
}

// StartSignIn implements AdminAPI. site picks the vendor flow ("device" for
// the ChatGPT device code); empty takes magpie's default.
func (a *HTTPAdmin) StartSignIn(ctx context.Context, agent, site string) (SignIn, error) {
	body := map[string]any{"agent": agent}
	if site != "" {
		body["site"] = site
	}
	var out SignIn
	err := a.do(ctx, http.MethodPost, a.WebURL, "/api/signin", body, &out)
	return out, err
}

// SignInStatus implements AdminAPI.
func (a *HTTPAdmin) SignInStatus(ctx context.Context, id string) (SignIn, error) {
	var out SignIn
	err := a.do(ctx, http.MethodGet, a.WebURL, "/api/signin/"+url.PathEscape(id), nil, &out)
	return out, err
}

// SubmitSignInCode implements AdminAPI: the code or the callback address
// the vendor page shows. magpie takes both in the "url" field.
func (a *HTTPAdmin) SubmitSignInCode(ctx context.Context, id, code string) error {
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/signin/"+url.PathEscape(id)+"/callback", map[string]any{"url": code}, nil)
}

// CancelSignIn implements AdminAPI.
func (a *HTTPAdmin) CancelSignIn(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/signin/"+url.PathEscape(id)+"/cancel", map[string]any{}, nil)
}

// AddSetupToken implements AdminAPI: a token from `claude setup-token`
// becomes a Claude account. It returns the account name magpie gives it.
func (a *HTTPAdmin) AddSetupToken(ctx context.Context, token string) (string, error) {
	var out struct {
		User string `json:"user"`
	}
	err := a.do(ctx, http.MethodPost, a.WebURL, "/api/signin/claude-token", map[string]any{"token": token}, &out)
	return out.User, err
}

// ForgetLogin implements AdminAPI: magpie removes the account and its
// stored sign-in.
func (a *HTTPAdmin) ForgetLogin(ctx context.Context, agent, user string) error {
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/login/forget", map[string]any{"agent": agent, "user": user}, nil)
}
