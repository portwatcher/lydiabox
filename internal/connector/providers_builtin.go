package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func githubProvider() Provider {
	return Provider{
		ID: GitHub, DisplayName: "GitHub",
		AuthURL:       "https://github.com/login/oauth/authorize",
		TokenURL:      "https://github.com/login/oauth/access_token",
		APIBase:       "https://api.github.com",
		DefaultScopes: []string{"repo", "read:org", "workflow"},
		ScopeParam:    "scope", ScopeSep: " ",
		GitHosts:   []string{"github.com"},
		tokenAuth:  authInForm,
		identify:   githubIdentify,
		parseToken: parseStandardToken,
		revoke:     githubRevoke,
	}
}

func gdriveProvider() Provider {
	return Provider{
		ID: GDrive, DisplayName: "Google Drive",
		AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:   "https://oauth2.googleapis.com/token",
		RevokeURL:  "https://oauth2.googleapis.com/revoke",
		APIBase:    "https://www.googleapis.com",
		UploadBase: "https://www.googleapis.com/upload",
		DefaultScopes: []string{
			"https://www.googleapis.com/auth/drive.file",
			"https://www.googleapis.com/auth/drive.readonly",
		},
		ScopeParam: "scope", ScopeSep: " ",
		// offline + consent makes Google return a refresh token.
		ExtraAuthParams: map[string]string{"access_type": "offline", "prompt": "consent", "include_granted_scopes": "true"},
		tokenAuth:       authInForm,
		identify:        gdriveIdentify,
		parseToken:      parseStandardToken,
		revoke:          gdriveRevoke,
	}
}

func notionProvider() Provider {
	return Provider{
		ID: Notion, DisplayName: "Notion",
		AuthURL:  "https://api.notion.com/v1/oauth/authorize",
		TokenURL: "https://api.notion.com/v1/oauth/token",
		APIBase:  "https://api.notion.com",
		// Notion has no OAuth scopes; capabilities are set on the integration.
		DefaultScopes: []string{},
		ScopeParam:    "", ScopeSep: " ",
		ExtraAuthParams: map[string]string{"owner": "user"},
		tokenAuth:       authBasicJSON,
		identify:        notionIdentify,
		parseToken:      parseStandardToken,
		revoke:          notionRevoke,
	}
}

func slackProvider() Provider {
	return Provider{
		ID: Slack, DisplayName: "Slack",
		AuthURL:  "https://slack.com/oauth/v2/authorize",
		TokenURL: "https://slack.com/api/oauth.v2.access",
		APIBase:  "https://slack.com/api",
		// User-token scopes: search.messages only accepts user tokens.
		DefaultScopes: []string{"channels:read", "groups:read", "chat:write", "search:read", "users:read"},
		ScopeParam:    "user_scope", ScopeSep: ",",
		tokenAuth:  authInForm,
		identify:   slackIdentify,
		parseToken: parseSlackToken,
		revoke:     slackRevoke,
	}
}

// tokenResponse is a parsed OAuth token endpoint answer.
type tokenResponse struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int64
	Scopes       []string
}

// oauthError is an RFC 6749 error answer. Code is safe to show and audit.
type oauthError struct {
	Code        string
	Description string
}

func (e *oauthError) Error() string {
	if e.Description == "" {
		return "oauth: " + e.Code
	}
	return "oauth: " + e.Code + ": " + truncate(e.Description, 200)
}

type standardToken struct {
	AccessToken      string          `json:"access_token"`
	RefreshToken     string          `json:"refresh_token"`
	TokenType        string          `json:"token_type"`
	ExpiresIn        json.Number     `json:"expires_in"`
	Scope            json.RawMessage `json:"scope"`
	Error            string          `json:"error"`
	ErrorDescription string          `json:"error_description"`
}

func (t standardToken) result() (tokenResponse, error) {
	if t.Error != "" {
		return tokenResponse{}, &oauthError{Code: t.Error, Description: t.ErrorDescription}
	}
	if t.AccessToken == "" {
		return tokenResponse{}, &oauthError{Code: "invalid_response", Description: "no access_token in token response"}
	}
	exp, _ := t.ExpiresIn.Int64()
	return tokenResponse{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, TokenType: t.TokenType,
		ExpiresIn: exp, Scopes: parseScopeField(t.Scope)}, nil
}

func parseStandardToken(body []byte) (tokenResponse, error) {
	var t standardToken
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&t); err != nil {
		return tokenResponse{}, &oauthError{Code: "invalid_response", Description: "token response is not JSON"}
	}
	return t.result()
}

// parseSlackToken reads oauth.v2.access, which nests the user token under
// authed_user and reports errors as {"ok":false,"error":"..."}.
func parseSlackToken(body []byte) (tokenResponse, error) {
	var t struct {
		standardToken
		OK         bool          `json:"ok"`
		AuthedUser standardToken `json:"authed_user"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&t); err != nil {
		return tokenResponse{}, &oauthError{Code: "invalid_response", Description: "token response is not JSON"}
	}
	if !t.OK {
		code := t.Error
		if code == "" {
			code = "invalid_response"
		}
		return tokenResponse{}, &oauthError{Code: code}
	}
	if t.AuthedUser.AccessToken != "" {
		return t.AuthedUser.result()
	}
	return t.standardToken.result()
}

// parseScopeField accepts "a b", "a,b", or ["a","b"].
func parseScopeField(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return splitScopeString(s)
}

func splitScopeString(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func githubIdentify(ctx context.Context, h *http.Client, p *Provider, token string) (Identity, error) {
	req, err := NewRequest(ctx, http.MethodGet, p.API("/user"), token, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.Do(req)
	if err != nil {
		return Identity{}, transportError(p.ID, err)
	}
	defer resp.Body.Close()
	var u struct {
		Login string `json:"login"`
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, &APIError{Provider: p.ID, Status: resp.StatusCode, Message: "token rejected by GitHub"}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil || u.Login == "" {
		return Identity{}, &APIError{Provider: p.ID, Status: resp.StatusCode, Message: "invalid user response"}
	}
	return Identity{Label: u.Login, Scopes: splitScopeString(resp.Header.Get("X-OAuth-Scopes"))}, nil
}

func gdriveIdentify(ctx context.Context, h *http.Client, p *Provider, token string) (Identity, error) {
	req, err := NewRequest(ctx, http.MethodGet, p.API("/drive/v3/about?fields=user"), token, nil)
	if err != nil {
		return Identity{}, err
	}
	var v struct {
		User struct {
			EmailAddress string `json:"emailAddress"`
			DisplayName  string `json:"displayName"`
		} `json:"user"`
	}
	if err := Do(h, req, p.ID, &v); err != nil {
		return Identity{}, err
	}
	label := v.User.EmailAddress
	if label == "" {
		label = v.User.DisplayName
	}
	if label == "" {
		return Identity{}, &APIError{Provider: p.ID, Message: "Drive did not report a user"}
	}
	return Identity{Label: label}, nil
}

func notionIdentify(ctx context.Context, h *http.Client, p *Provider, token string) (Identity, error) {
	req, err := NewRequest(ctx, http.MethodGet, p.API("/v1/users/me"), token, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Notion-Version", NotionVersion)
	var v struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
			Owner         struct {
				User struct {
					Name string `json:"name"`
				} `json:"user"`
			} `json:"owner"`
		} `json:"bot"`
	}
	if err := Do(h, req, p.ID, &v); err != nil {
		return Identity{}, err
	}
	for _, l := range []string{v.Bot.WorkspaceName, v.Bot.Owner.User.Name, v.Name, v.ID} {
		if l != "" {
			return Identity{Label: l}, nil
		}
	}
	return Identity{}, &APIError{Provider: p.ID, Message: "Notion did not report a user"}
}

// SlackResponse is the envelope of every Slack Web API answer.
type SlackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// SlackCall performs one Slack Web API call and maps {"ok":false} to an
// *APIError. Slack answers HTTP 200 for most failures.
func SlackCall(h *http.Client, req *http.Request, out any) error {
	var raw json.RawMessage
	if err := Do(h, req, Slack, &raw); err != nil {
		return err
	}
	var env SlackResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return &APIError{Provider: Slack, Message: "invalid JSON response"}
	}
	if !env.OK {
		status := http.StatusBadRequest
		switch env.Error {
		case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive":
			status = http.StatusUnauthorized
		}
		return &APIError{Provider: Slack, Status: status, Message: env.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &APIError{Provider: Slack, Message: "invalid JSON response"}
	}
	return nil
}

func slackIdentify(ctx context.Context, h *http.Client, p *Provider, token string) (Identity, error) {
	req, err := NewRequest(ctx, http.MethodPost, p.API("/auth.test"), token, nil)
	if err != nil {
		return Identity{}, err
	}
	var v struct {
		User string `json:"user"`
		Team string `json:"team"`
	}
	if err := SlackCall(h, req, &v); err != nil {
		return Identity{}, err
	}
	if v.User == "" {
		return Identity{}, &APIError{Provider: p.ID, Message: "Slack did not report a user"}
	}
	label := v.User
	if v.Team != "" {
		label += "@" + v.Team
	}
	return Identity{Label: label}, nil
}

// errNoClient means the revoke call needs OAuth client credentials that are
// not available (relay mode or a pasted token).
var errNoClient = errors.New("revoke needs the OAuth client credentials")

func githubRevoke(ctx context.Context, h *http.Client, p *Provider, client OAuthClient, token string) error {
	if client.ClientID == "" || client.ClientSecret == "" {
		return errNoClient
	}
	body, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := NewRequest(ctx, http.MethodDelete, p.API("/applications/"+url.PathEscape(client.ClientID)+"/grant"), "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(client.ClientID, client.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	return Do(h, req, p.ID, nil)
}

func gdriveRevoke(ctx context.Context, h *http.Client, p *Provider, _ OAuthClient, token string) error {
	req, err := NewRequest(ctx, http.MethodPost, p.RevokeURL, "", strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return Do(h, req, p.ID, nil)
}

func slackRevoke(ctx context.Context, h *http.Client, p *Provider, _ OAuthClient, token string) error {
	req, err := NewRequest(ctx, http.MethodPost, p.API("/auth.revoke"), token, nil)
	if err != nil {
		return err
	}
	return SlackCall(h, req, nil)
}

// notionRevoke calls POST /v1/oauth/revoke with the client credentials as
// HTTP Basic auth (developers.notion.com/reference/revoke-token).
func notionRevoke(ctx context.Context, h *http.Client, p *Provider, client OAuthClient, token string) error {
	if client.ClientID == "" || client.ClientSecret == "" {
		return errNoClient
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	req, err := NewRequest(ctx, http.MethodPost, p.API("/v1/oauth/revoke"), "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(client.ClientID, client.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", NotionVersion)
	return Do(h, req, p.ID, nil)
}
