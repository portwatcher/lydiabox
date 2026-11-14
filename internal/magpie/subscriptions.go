package magpie

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Subscription kinds: the harness subscriptions a user can sign in to.
// magpie keeps the sign-in (access and refresh tokens) in its data
// directory on the host and serves the subscription as a provider; the
// harness in the VM keeps the gateway URL and the placeholder token.
const (
	SubscriptionClaude = "claude"
	SubscriptionCodex  = "codex"
)

// subscriptionKinds lists the kinds in display order with their magpie
// sign-in flow. Codex always uses the ChatGPT device code: the browser
// that signs in is not on the host, so the localhost callback of the
// browser flow cannot reach magpie.
var subscriptionKinds = []struct {
	kind, name, site string
}{
	{SubscriptionClaude, "Claude", ""},
	{SubscriptionCodex, "ChatGPT", "device"},
}

// SubscriptionAccount is one signed-in account of a subscription.
type SubscriptionAccount struct {
	User    string `json:"user"`
	Plan    string `json:"plan"`
	Enabled bool   `json:"enabled"`
	// NeedsSignIn is set when the vendor refused the stored sign-in.
	NeedsSignIn bool `json:"needsSignIn"`
}

// Subscription is the API view of one subscription kind. ProviderID is
// empty until an account is signed in.
type Subscription struct {
	Kind       string                `json:"kind"`
	Name       string                `json:"name"`
	ProviderID string                `json:"providerId"`
	Enabled    bool                  `json:"enabled"`
	Accounts   []SubscriptionAccount `json:"accounts"`
	Models     []string              `json:"models"`
}

// SubscriptionLogin is a sign-in under way. It carries no token.
type SubscriptionLogin struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	URL   string `json:"url"`
	// UserCode is the device code to enter on the vendor page.
	UserCode string `json:"userCode"`
	// NeedsCode asks for the code or address the vendor page shows at the
	// end (POST .../logins/{id}/code).
	NeedsCode  bool   `json:"needsCode"`
	Installing string `json:"installing"`
	Account    string `json:"account"`
	Error      string `json:"error"`
}

// StartLoginRequest is the body of POST /api/v1/models/subscriptions/logins.
// SetupToken (Claude only) adds the account from `claude setup-token`
// without a browser sign-in.
type StartLoginRequest struct {
	Kind       string `json:"kind"`
	SetupToken string `json:"setupToken"`
}

var loginIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func subscriptionKind(kind string) (name, site string, ok bool) {
	for _, k := range subscriptionKinds {
		if k.kind == kind {
			return k.name, k.site, true
		}
	}
	return "", "", false
}

func checkKind(kind string) error {
	if _, _, ok := subscriptionKind(kind); !ok {
		return fmt.Errorf("%w: kind must be %q or %q", ErrBadRequest, SubscriptionClaude, SubscriptionCodex)
	}
	return nil
}

func checkLoginID(id string) error {
	if !loginIDRe.MatchString(id) {
		return fmt.Errorf("%w: login %q", ErrNotFound, id)
	}
	return nil
}

// ListSubscriptions returns one entry per subscription kind.
func (s *Service) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	rps, err := a.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Subscription, 0, len(subscriptionKinds))
	for _, k := range subscriptionKinds {
		sub := Subscription{Kind: k.kind, Name: k.name, Accounts: []SubscriptionAccount{}, Models: []string{}}
		i := slices.IndexFunc(rps, func(p RemoteProvider) bool { return p.Account != nil && p.Account.Agent == k.kind })
		if i >= 0 {
			p := rps[i]
			sub.ProviderID, sub.Enabled = p.ID, !p.Off
			if p.Models != nil {
				sub.Models = p.Models
			}
			for _, l := range p.Account.Logins {
				sub.Accounts = append(sub.Accounts, SubscriptionAccount{User: l.User, Plan: l.Plan,
					Enabled: (l.Active || l.On) && !l.Paused && !l.Lapsed, NeedsSignIn: l.Lapsed})
			}
		}
		out = append(out, sub)
	}
	return out, nil
}

func toLogin(kind string, si SignIn) SubscriptionLogin {
	if kind == "" {
		kind = si.Agent
	}
	return SubscriptionLogin{ID: si.ID, Kind: kind, State: si.State, URL: si.URL, UserCode: si.Code,
		NeedsCode: si.PasteCode || si.PasteCallback, Installing: si.Installing, Account: si.User, Error: si.Error}
}

// StartLogin starts a subscription sign-in, or adds a Claude account from
// a setup token.
func (s *Service) StartLogin(ctx context.Context, req StartLoginRequest) (SubscriptionLogin, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if err := checkKind(req.Kind); err != nil {
		return SubscriptionLogin{}, err
	}
	token := strings.TrimSpace(req.SetupToken)
	if token != "" && req.Kind != SubscriptionClaude {
		return SubscriptionLogin{}, fmt.Errorf("%w: setupToken is only for kind %q", ErrBadRequest, SubscriptionClaude)
	}
	if len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return SubscriptionLogin{}, fmt.Errorf("%w: setupToken is malformed", ErrBadRequest)
	}
	a, err := s.admin()
	if err != nil {
		return SubscriptionLogin{}, err
	}
	if token != "" {
		user, err := a.AddSetupToken(ctx, token)
		if err != nil {
			return SubscriptionLogin{}, err
		}
		return SubscriptionLogin{Kind: req.Kind, State: SignInDone, Account: user}, nil
	}
	_, site, _ := subscriptionKind(req.Kind)
	si, err := a.StartSignIn(ctx, req.Kind, site)
	if err != nil {
		return SubscriptionLogin{}, err
	}
	return toLogin(req.Kind, si), nil
}

// LoginStatus returns where a sign-in stands.
func (s *Service) LoginStatus(ctx context.Context, id string) (SubscriptionLogin, error) {
	if err := checkLoginID(id); err != nil {
		return SubscriptionLogin{}, err
	}
	a, err := s.admin()
	if err != nil {
		return SubscriptionLogin{}, err
	}
	si, err := a.SignInStatus(ctx, id)
	if err != nil {
		return SubscriptionLogin{}, err
	}
	return toLogin("", si), nil
}

// SubmitLoginCode completes a sign-in with the code or address the vendor
// page shows, and returns its new state.
func (s *Service) SubmitLoginCode(ctx context.Context, id, code string) (SubscriptionLogin, error) {
	if err := checkLoginID(id); err != nil {
		return SubscriptionLogin{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 8192 {
		return SubscriptionLogin{}, fmt.Errorf("%w: code is required", ErrBadRequest)
	}
	a, err := s.admin()
	if err != nil {
		return SubscriptionLogin{}, err
	}
	if err := a.SubmitSignInCode(ctx, id, code); err != nil {
		return SubscriptionLogin{}, err
	}
	si, err := a.SignInStatus(ctx, id)
	if err != nil {
		return SubscriptionLogin{}, err
	}
	return toLogin("", si), nil
}

// CancelLogin gives up a sign-in.
func (s *Service) CancelLogin(ctx context.Context, id string) error {
	if err := checkLoginID(id); err != nil {
		return err
	}
	a, err := s.admin()
	if err != nil {
		return err
	}
	return a.CancelSignIn(ctx, id)
}

// RemoveAccount signs a subscription account out: magpie deletes the
// stored sign-in.
func (s *Service) RemoveAccount(ctx context.Context, kind, user string) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	subs, err := s.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Kind != kind {
			continue
		}
		for _, acc := range sub.Accounts {
			if acc.User == user {
				a, err := s.admin()
				if err != nil {
					return err
				}
				return a.ForgetLogin(ctx, kind, user)
			}
		}
	}
	return fmt.Errorf("%w: %s account %q", ErrNotFound, kind, user)
}
