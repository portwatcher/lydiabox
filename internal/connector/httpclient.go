package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxResponseBytes bounds JSON responses read from providers.
const MaxResponseBytes = 32 << 20

// APIError is a failed upstream call. Message is taken from the provider's
// error fields only, so it never echoes request credentials.
type APIError struct {
	Provider string
	Status   int
	Message  string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s", e.Provider, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Provider, e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}

// Unauthorized reports whether the provider rejected the credential.
func (e *APIError) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// NewHTTPClient returns the default outbound client: bounded timeouts and no
// redirects to other hosts carrying the Authorization header (net/http drops
// it on cross-host redirects).
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// Do sends req and decodes a JSON response into out (unless out is nil). A
// non-2xx status becomes an *APIError. Transport errors are reduced to their
// operation so that URLs with query secrets never reach logs.
func Do(h *http.Client, req *http.Request, provider string, out any) error {
	resp, err := h.Do(req)
	if err != nil {
		return transportError(provider, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return transportError(provider, err)
	}
	if len(body) > MaxResponseBytes {
		return &APIError{Provider: provider, Status: resp.StatusCode, Message: "response too large"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Provider: provider, Status: resp.StatusCode, Message: upstreamMessage(body)}
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &APIError{Provider: provider, Status: resp.StatusCode, Message: "invalid JSON response"}
	}
	return nil
}

// transportError strips the request URL from *url.Error.
func transportError(provider string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &APIError{Provider: provider, Message: "request failed: " + err.Error()}
}

// upstreamMessage extracts a short error description from a provider error
// body ({"message"}, {"error_description"}, {"error":"..."}, or
// {"error":{"message"}}).
func upstreamMessage(body []byte) string {
	var v struct {
		Message          string          `json:"message"`
		ErrorDescription string          `json:"error_description"`
		Error            json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	msg := v.Message
	if msg == "" {
		msg = v.ErrorDescription
	}
	if msg == "" && len(v.Error) > 0 {
		var s string
		var o struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &s) == nil {
			msg = s
		} else if json.Unmarshal(v.Error, &o) == nil {
			msg = o.Message
		}
	}
	return truncate(msg, 300)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// NewRequest builds a request with a bearer token and JSON accept header.
func NewRequest(ctx context.Context, method, rawURL, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "lydiabox-connector")
	return req, nil
}
