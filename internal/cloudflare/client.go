// Package cloudflare sets up remote access to LydiaBox through Cloudflare
// Zero Trust: a typed client for the parts of the Cloudflare v4 API that
// LydiaBox uses (tunnels, DNS, Access), and the idempotent Setup and
// Teardown flows of `lydiabox cloudflare`. The subpackages verify Access
// JWTs (accessjwt) and supervise the cloudflared connector (cloudflared).
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Cloudflare v4 API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Options configures a Client.
type Options struct {
	// BaseURL defaults to DefaultBaseURL. Tests point it at a fake API.
	BaseURL string
	// Token is an API token with the permissions listed in
	// docs/remote-access-manual-test.md.
	Token string
	// AccountID is the Cloudflare account that owns the tunnel and the
	// Access application.
	AccountID  string
	HTTPClient *http.Client
}

// Client calls the Cloudflare v4 API for one account.
type Client struct {
	base      string
	token     string
	accountID string
	http      *http.Client
}

// NewClient validates opts.
func NewClient(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("cloudflare: API token is required")
	}
	if strings.TrimSpace(opts.AccountID) == "" {
		return nil, errors.New("cloudflare: account ID is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		base:      strings.TrimRight(opts.BaseURL, "/"),
		token:     strings.TrimSpace(opts.Token),
		accountID: strings.TrimSpace(opts.AccountID),
		http:      opts.HTTPClient,
	}, nil
}

// AccountID returns the account of the client.
func (c *Client) AccountID() string { return c.accountID }

// Message is one entry of the errors or messages array of an API response.
type Message struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// APIError is a failed API call.
type APIError struct {
	Method string
	Path   string
	Status int
	Errors []Message
}

func (e *APIError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, m := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d: %s", m.Code, m.Message))
	}
	detail := strings.Join(parts, "; ")
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("cloudflare: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, detail)
}

// IsNotFound reports whether err is an API 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []Message       `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *resultInfo     `json:"result_info"`
}

// maxResponse caps one API response body.
const maxResponse = 16 << 20

// do sends one request and decodes the result into out (when non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) (*resultInfo, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: encode %s %s: %w", method, path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: read %s %s: %w", method, path, err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		if resp.StatusCode >= 300 {
			return nil, &APIError{Method: method, Path: path, Status: resp.StatusCode}
		}
		return nil, fmt.Errorf("cloudflare: decode %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 || !env.Success {
		status := resp.StatusCode
		if status < 300 {
			status = http.StatusBadGateway
		}
		return nil, &APIError{Method: method, Path: path, Status: status, Errors: env.Errors}
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare: decode %s %s result: %w", method, path, err)
		}
	}
	return env.ResultInfo, nil
}

// maxPages bounds pagination so a misbehaving API cannot loop forever.
const maxPages = 100

// list collects every page of a paginated list endpoint.
func list[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("per_page", "50")
	var all []T
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var items []T
		info, err := c.do(ctx, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if info == nil || len(items) == 0 || (info.TotalPages > 0 && page >= info.TotalPages) ||
			(info.TotalPages == 0 && len(items) < 50) {
			return all, nil
		}
	}
	return all, nil
}

func (c *Client) accountPath(format string, args ...any) string {
	return "/accounts/" + url.PathEscape(c.accountID) + fmt.Sprintf(format, args...)
}
