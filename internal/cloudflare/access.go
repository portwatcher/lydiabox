package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Organization is the Zero Trust organization of the account.
type Organization struct {
	Name string `json:"name"`
	// AuthDomain is the team domain, for example myteam.cloudflareaccess.com.
	AuthDomain string `json:"auth_domain"`
}

// AccessOrganization returns the Zero Trust organization. Cloudflare creates
// it when the owner enables Zero Trust and picks a team name in the
// dashboard; the API cannot pick the plan, so Setup reports a clear error
// instead.
func (c *Client) AccessOrganization(ctx context.Context) (Organization, error) {
	var org Organization
	_, err := c.do(ctx, http.MethodGet, c.accountPath("/access/organizations"), nil, nil, &org)
	if err != nil {
		return org, err
	}
	if org.AuthDomain == "" {
		return org, fmt.Errorf("cloudflare: Zero Trust has no team domain yet; open the Cloudflare dashboard, enable Zero Trust, and pick a team name")
	}
	return org, nil
}

// IdentityProvider is an Access login method.
type IdentityProvider struct {
	ID     string         `json:"id,omitempty"`
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// IdentityProviderOTP is the type of the one-time PIN login method.
const IdentityProviderOTP = "onetimepin"

// IdentityProviders lists the Access login methods.
func (c *Client) IdentityProviders(ctx context.Context) ([]IdentityProvider, error) {
	return list[IdentityProvider](ctx, c, c.accountPath("/access/identity_providers"), nil)
}

// CreateIdentityProvider adds an Access login method.
func (c *Client) CreateIdentityProvider(ctx context.Context, idp IdentityProvider) (IdentityProvider, error) {
	if idp.Config == nil {
		idp.Config = map[string]any{}
	}
	var out IdentityProvider
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/access/identity_providers"), nil, idp, &out)
	return out, err
}

// Rule is one include rule of an Access policy, for example
// {"email": {"email": "me@example.com"}}.
type Rule map[string]map[string]string

// EmailRules turns an allow list into include rules. An entry that starts
// with "@" allows the whole email domain.
func EmailRules(emails []string) []Rule {
	rules := make([]Rule, 0, len(emails))
	for _, e := range emails {
		if domain, ok := strings.CutPrefix(e, "@"); ok {
			rules = append(rules, Rule{"email_domain": {"domain": domain}})
			continue
		}
		rules = append(rules, Rule{"email": {"email": e}})
	}
	return rules
}

// Policy decisions.
const (
	DecisionAllow       = "allow"
	DecisionNonIdentity = "non_identity"
	DecisionBypass      = "bypass"
)

// Policy is a reusable Access policy.
type Policy struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name"`
	Decision        string `json:"decision"`
	Include         []Rule `json:"include"`
	SessionDuration string `json:"session_duration,omitempty"`
}

// Policies lists the reusable Access policies.
func (c *Client) Policies(ctx context.Context) ([]Policy, error) {
	return list[Policy](ctx, c, c.accountPath("/access/policies"), nil)
}

// CreatePolicy adds a reusable Access policy.
func (c *Client) CreatePolicy(ctx context.Context, p Policy) (Policy, error) {
	var out Policy
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/access/policies"), nil, p, &out)
	return out, err
}

// UpdatePolicy replaces a reusable Access policy.
func (c *Client) UpdatePolicy(ctx context.Context, id string, p Policy) (Policy, error) {
	p.ID = ""
	var out Policy
	_, err := c.do(ctx, http.MethodPut, c.accountPath("/access/policies/%s", esc(id)), nil, p, &out)
	return out, err
}

// DeletePolicy removes a reusable Access policy.
func (c *Client) DeletePolicy(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, c.accountPath("/access/policies/%s", esc(id)), nil, nil, nil)
	return err
}

// PolicyRef attaches a reusable policy to an application.
type PolicyRef struct {
	ID         string `json:"id"`
	Precedence int    `json:"precedence"`
}

// Application is a self-hosted Access application.
type Application struct {
	ID                 string      `json:"id,omitempty"`
	Name               string      `json:"name"`
	Domain             string      `json:"domain"`
	Type               string      `json:"type"`
	SessionDuration    string      `json:"session_duration,omitempty"`
	AppLauncherVisible bool        `json:"app_launcher_visible"`
	Policies           []PolicyRef `json:"policies"`
	// AUD is the audience tag that Access puts in the JWT "aud" claim.
	AUD string `json:"aud,omitempty"`
}

// Applications lists the Access applications.
func (c *Client) Applications(ctx context.Context) ([]Application, error) {
	return list[Application](ctx, c, c.accountPath("/access/apps"), nil)
}

// CreateApplication adds an Access application.
func (c *Client) CreateApplication(ctx context.Context, app Application) (Application, error) {
	var out Application
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/access/apps"), nil, app, &out)
	return out, err
}

// UpdateApplication replaces an Access application.
func (c *Client) UpdateApplication(ctx context.Context, id string, app Application) (Application, error) {
	app.ID, app.AUD = "", ""
	var out Application
	_, err := c.do(ctx, http.MethodPut, c.accountPath("/access/apps/%s", esc(id)), nil, app, &out)
	return out, err
}

// DeleteApplication removes an Access application.
func (c *Client) DeleteApplication(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, c.accountPath("/access/apps/%s", esc(id)), nil, nil, nil)
	return err
}

// ServiceToken is an Access service token for non-browser clients.
type ServiceToken struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret is returned only by create and rotate.
	ClientSecret string `json:"client_secret,omitempty"`
	Duration     string `json:"duration,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// ServiceTokens lists the Access service tokens.
func (c *Client) ServiceTokens(ctx context.Context) ([]ServiceToken, error) {
	return list[ServiceToken](ctx, c, c.accountPath("/access/service_tokens"), nil)
}

// CreateServiceToken adds a service token.
func (c *Client) CreateServiceToken(ctx context.Context, name, duration string) (ServiceToken, error) {
	var out ServiceToken
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/access/service_tokens"), nil,
		ServiceToken{Name: name, Duration: duration}, &out)
	return out, err
}

// RotateServiceToken issues a new client secret for a service token.
func (c *Client) RotateServiceToken(ctx context.Context, id string) (ServiceToken, error) {
	var out ServiceToken
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/access/service_tokens/%s/rotate", esc(id)), nil, nil, &out)
	return out, err
}

// DeleteServiceToken removes a service token.
func (c *Client) DeleteServiceToken(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, c.accountPath("/access/service_tokens/%s", esc(id)), nil, nil, nil)
	return err
}
