package magpietest

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lydiabox/lydiabox/internal/magpie"
)

// Fake subscription values. GoodCode completes a sign-in that asks for a
// pasted code; the tokens are what the fake "receives" from the vendor
// and keeps in memory only, as magpie keeps them on the host.
const (
	GoodCode          = "fake-code#fake-state"
	FakeAccessToken   = "sk-ant-oat01-fake-access-token"
	FakeRefreshToken  = "sk-ant-ort01-fake-refresh-token"
	FakeCodexRefresh  = "rt_fake-codex-refresh-token"
	ClaudeProviderID  = "claude"
	CodexProviderID   = "codex"
	claudeSignInURL   = "https://claude.ai/oauth/authorize?code=true&client_id=fake"
	codexDeviceURL    = "https://auth.openai.com/codex/device"
	codexDeviceCode   = "ABCD-1234"
	setupTokenAccount = "Claude setup-token "
)

type fakeSignIn struct {
	magpie.SignIn
}

// subscriptionProvider returns the provider of a subscription agent,
// adding it when absent.
func (f *FakeAdmin) subscriptionProvider(agent string) *magpie.RemoteProvider {
	for i := range f.Providers {
		if a := f.Providers[i].Account; a != nil && a.Agent == agent {
			return &f.Providers[i]
		}
	}
	p := magpie.RemoteProvider{ID: CodexProviderID, Name: "Codex", Responses: "https://chatgpt.com/backend-api/codex",
		Ready: true, Models: []string{"gpt-5-codex"}, Account: &magpie.RemoteAccount{Agent: agent}}
	if agent == "claude" {
		p = magpie.RemoteProvider{ID: ClaudeProviderID, Name: "Claude Code", Anthropic: "https://api.anthropic.com",
			Ready: true, Models: []string{"claude-sonnet-4-5", "claude-opus-4-1"}, Account: &magpie.RemoteAccount{Agent: agent}}
	}
	f.Providers = append(f.Providers, p)
	return &f.Providers[len(f.Providers)-1]
}

// addLogin signs an account in and stores its tokens in the fake.
func (f *FakeAdmin) addLogin(agent, user, plan string, tokens ...string) {
	p := f.subscriptionProvider(agent)
	p.Account.Logins = slices.DeleteFunc(p.Account.Logins, func(l magpie.RemoteLogin) bool { return l.User == user })
	p.Account.Logins = append(p.Account.Logins, magpie.RemoteLogin{User: user, Plan: plan, Active: len(p.Account.Logins) == 0, On: true})
	if f.Tokens == nil {
		f.Tokens = map[string][]string{}
	}
	f.Tokens[agent+"/"+user] = tokens
}

// StartSignIn implements magpie.AdminAPI.
func (f *FakeAdmin) StartSignIn(_ context.Context, agent, site string) (magpie.SignIn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("StartSignIn " + agent + " " + site); err != nil {
		return magpie.SignIn{}, err
	}
	f.nextID++
	si := magpie.SignIn{ID: "si" + strconv.Itoa(f.nextID), Agent: agent, State: magpie.SignInWaiting}
	switch agent {
	case "claude":
		si.URL, si.PasteCode = claudeSignInURL, true
	case "codex":
		if site != "device" {
			return magpie.SignIn{}, fmt.Errorf("%w: the fake only has the ChatGPT device code", magpie.ErrBadRequest)
		}
		si.URL, si.Code = codexDeviceURL, codexDeviceCode
	default:
		return magpie.SignIn{}, fmt.Errorf("%w: unknown agent %q", magpie.ErrBadRequest, agent)
	}
	if f.signIns == nil {
		f.signIns = map[string]*fakeSignIn{}
	}
	f.signIns[si.ID] = &fakeSignIn{si}
	return si, nil
}

func (f *FakeAdmin) signIn(id string) (*fakeSignIn, error) {
	si, ok := f.signIns[id]
	if !ok {
		return nil, fmt.Errorf("%w: sign-in %s", magpie.ErrNotFound, id)
	}
	return si, nil
}

// SignInStatus implements magpie.AdminAPI.
func (f *FakeAdmin) SignInStatus(_ context.Context, id string) (magpie.SignIn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SignInStatus"); err != nil {
		return magpie.SignIn{}, err
	}
	si, err := f.signIn(id)
	if err != nil {
		return magpie.SignIn{}, err
	}
	return si.SignIn, nil
}

// SubmitSignInCode implements magpie.AdminAPI.
func (f *FakeAdmin) SubmitSignInCode(_ context.Context, id, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SubmitSignInCode"); err != nil {
		return err
	}
	si, err := f.signIn(id)
	if err != nil {
		return err
	}
	if !si.PasteCode || si.State != magpie.SignInWaiting {
		return fmt.Errorf("%w: this sign-in can't be finished from a pasted address", magpie.ErrBadRequest)
	}
	if code != GoodCode {
		return fmt.Errorf("%w: the code was refused", magpie.ErrBadRequest)
	}
	si.State, si.User = magpie.SignInDone, "claude-user@example.com"
	f.addLogin(si.Agent, si.User, "max", FakeAccessToken, FakeRefreshToken)
	return nil
}

// CompleteDeviceSignIn plays the user entering the device code on the
// vendor page: the sign-in is done and the account is added.
func (f *FakeAdmin) CompleteDeviceSignIn(id, user, plan string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	si, err := f.signIn(id)
	if err != nil {
		return err
	}
	si.State, si.User = magpie.SignInDone, user
	f.addLogin(si.Agent, user, plan, FakeCodexRefresh)
	return nil
}

// CancelSignIn implements magpie.AdminAPI.
func (f *FakeAdmin) CancelSignIn(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CancelSignIn"); err != nil {
		return err
	}
	si, err := f.signIn(id)
	if err != nil {
		return err
	}
	if si.State == magpie.SignInWaiting || si.State == magpie.SignInInstalling {
		si.State = magpie.SignInCanceled
	}
	return nil
}

// AddSetupToken implements magpie.AdminAPI.
func (f *FakeAdmin) AddSetupToken(_ context.Context, token string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AddSetupToken"); err != nil {
		return "", err
	}
	if len(token) < 8 {
		return "", fmt.Errorf("%w: not a setup-token", magpie.ErrBadRequest)
	}
	user := setupTokenAccount + token[len(token)-6:]
	f.addLogin("claude", user, "", token)
	return user, nil
}

// ForgetLogin implements magpie.AdminAPI. As magpie, it keeps the only
// Claude account that its own Claude Code copy is signed in to.
func (f *FakeAdmin) ForgetLogin(_ context.Context, agent, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ForgetLogin"); err != nil {
		return err
	}
	p := f.subscriptionProvider(agent)
	i := slices.IndexFunc(p.Account.Logins, func(l magpie.RemoteLogin) bool { return l.User == user })
	if i < 0 {
		return fmt.Errorf("%w: account %s", magpie.ErrNotFound, user)
	}
	if agent == "claude" && len(p.Account.Logins) == 1 && !strings.HasPrefix(user, setupTokenAccount) {
		return fmt.Errorf("%w: Claude Code is signed in to %s, the only account in magpie it can be signed in to", magpie.ErrConflict, user)
	}
	p.Account.Logins = slices.Delete(p.Account.Logins, i, i+1)
	delete(f.Tokens, agent+"/"+user)
	if len(p.Account.Logins) == 0 {
		f.Providers = slices.DeleteFunc(f.Providers, func(rp magpie.RemoteProvider) bool { return rp.Account != nil && rp.Account.Agent == agent })
	}
	return nil
}

// StoredTokens returns every subscription token the fake holds, to check
// that none reaches a response or the guest disk.
func (f *FakeAdmin) StoredTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ts := range f.Tokens {
		out = append(out, ts...)
	}
	return out
}
