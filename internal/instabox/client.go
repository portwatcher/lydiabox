// Package instabox is a typed client for the InstaBox REST API
// (https://api.instabox.sh, OpenAPI document /v1/openapi.json): hosted
// Firecracker machines that LydiaBox Cloud runs guests on. It covers the
// calls a VM driver and an operator need: machine lifecycle and
// operations, idle pause and maximum run, exec with streamed output,
// file upload and download, private ports, snapshots, and the CLI-only
// snapshot import of an OCI image.
//
// The client sends the Idempotency-Key header on every call that the API
// requires it for, retries transient failures only where a retry cannot
// repeat a side effect, maps error bodies to *APIError (which matches the
// Err* sentinels with errors.Is), and never logs or formats the API key.
//
// docs/instabox.md lists the API facts this package relies on and the
// ones not yet checked against the real service.
package instabox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the InstaBox API; paths start with /v1.
const DefaultBaseURL = "https://api.instabox.sh"

// Default retry settings.
const (
	DefaultMaxRetries     = 4
	DefaultRetryBaseDelay = 500 * time.Millisecond
	DefaultMaxRetryDelay  = 30 * time.Second
	// defaultCallTimeout bounds one JSON call (not exec, files, or the
	// operation long poll, which have their own bounds).
	defaultCallTimeout = 90 * time.Second
	maxErrorBody       = 64 << 10
	maxJSONBody        = 32 << 20
)

// Options configures a Client.
type Options struct {
	// BaseURL defaults to DefaultBaseURL. Tests point it at the fake in
	// package instaboxtest.
	BaseURL string
	// APIKey is an InstaBox API key (ibk_...), sent as X-API-Key.
	APIKey string
	// HTTPClient defaults to a client without an overall timeout: exec
	// and file transfers run long, and every call has a context.
	HTTPClient *http.Client
	// MaxRetries is the number of retries after the first attempt;
	// 0 means DefaultMaxRetries and a negative value disables retries.
	MaxRetries int
	// RetryBaseDelay is the first backoff delay (doubled per retry).
	RetryBaseDelay time.Duration
	// MaxRetryDelay caps one backoff delay. A Retry-After that is longer
	// is not waited for: the error goes back to the caller.
	MaxRetryDelay time.Duration
	// UserAgent defaults to "lydiabox-instabox".
	UserAgent string
	// Logger gets one debug line per attempt (method, path, status,
	// attempt). It never sees headers or bodies.
	Logger *slog.Logger
	// Sleep waits between retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client calls the InstaBox API with one API key. It is safe for
// concurrent use.
type Client struct {
	base       string
	apiKey     string
	http       *http.Client
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
	userAgent  string
	log        *slog.Logger
	sleep      func(ctx context.Context, d time.Duration) error
}

// NewClient validates opts.
func NewClient(opts Options) (*Client, error) {
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		return nil, errors.New("instabox: API key is required")
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("instabox: invalid base URL %q", opts.BaseURL)
	}
	// The API key must not go over plain HTTP, except to a local test
	// server.
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("instabox: base URL %q must use https", opts.BaseURL)
	}
	c := &Client{
		base:       strings.TrimSuffix(base, "/v1"),
		apiKey:     key,
		http:       opts.HTTPClient,
		maxRetries: opts.MaxRetries,
		baseDelay:  opts.RetryBaseDelay,
		maxDelay:   opts.MaxRetryDelay,
		userAgent:  opts.UserAgent,
		log:        opts.Logger,
		sleep:      opts.Sleep,
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	c.http = keepKeyOnRedirect(c.http)
	if c.maxRetries == 0 {
		c.maxRetries = DefaultMaxRetries
	}
	if c.maxRetries < 0 {
		c.maxRetries = 0
	}
	if c.baseDelay <= 0 {
		c.baseDelay = DefaultRetryBaseDelay
	}
	if c.maxDelay <= 0 {
		c.maxDelay = DefaultMaxRetryDelay
	}
	if c.userAgent == "" {
		c.userAgent = "lydiabox-instabox"
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	return c, nil
}

// String redacts the API key, so that %v of a Client is safe to log.
func (c *Client) String() string { return "instabox.Client{" + c.base + "}" }

// GoString redacts the API key for %#v.
func (c *Client) GoString() string { return c.String() }

// BaseURL returns the API base URL without /v1.
func (c *Client) BaseURL() string { return c.base }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type idemKey struct{}

// WithIdempotencyKey makes the next mutating call made with ctx send key
// instead of a random one. Save the key before the call, so a retry after
// a crash replays the same request instead of acting twice.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idemKey{}, key)
}

// NewIdempotencyKey returns a random key for WithIdempotencyKey.
func NewIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "lbx-" + hex.EncodeToString(b[:])
}

func idempotencyKey(ctx context.Context) string {
	if k, ok := ctx.Value(idemKey{}).(string); ok && k != "" {
		return k
	}
	return NewIdempotencyKey()
}

// retryMode says which failures a call may retry.
type retryMode int

const (
	// retrySafe: transport errors and responses the server marks
	// retryable. For GET and for calls with an Idempotency-Key.
	retrySafe retryMode = iota
	// retryNotStarted: only error responses that prove the request did
	// nothing (rate and concurrency limits, a machine still waking).
	// For exec, which has no Idempotency-Key.
	retryNotStarted
	// retryNever: one attempt.
	retryNever
)

type request struct {
	method      string
	path        string
	query       url.Values
	jsonBody    any
	body        io.Reader // raw body; retried only when it is an io.Seeker
	contentType string
	accept      string
	idempotent  bool
	header      map[string]string
	retry       retryMode
}

// do sends req and returns a 2xx response, whose body the caller closes.
func (c *Client) do(ctx context.Context, req request) (*http.Response, error) {
	var payload []byte
	if req.jsonBody != nil {
		b, err := json.Marshal(req.jsonBody)
		if err != nil {
			return nil, fmt.Errorf("instabox: encode %s %s: %w", req.method, req.path, err)
		}
		payload = b
		req.contentType = "application/json"
	}
	seeker, _ := req.body.(io.Seeker)
	if req.body != nil && seeker == nil {
		req.retry = retryNever
	}
	var key string
	if req.idempotent {
		key = idempotencyKey(ctx)
	}
	target := c.base + req.path
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	for attempt := 0; ; attempt++ {
		var body io.Reader
		switch {
		case payload != nil:
			body = bytes.NewReader(payload)
		case req.body != nil:
			if attempt > 0 {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					return nil, fmt.Errorf("instabox: rewind body of %s %s: %w", req.method, req.path, err)
				}
			}
			body = req.body
		}
		hr, err := http.NewRequestWithContext(ctx, req.method, target, body)
		if err != nil {
			return nil, fmt.Errorf("instabox: %s %s: %w", req.method, req.path, err)
		}
		hr.Header.Set("X-API-Key", c.apiKey)
		hr.Header.Set("User-Agent", c.userAgent)
		if req.contentType != "" {
			hr.Header.Set("Content-Type", req.contentType)
		}
		if req.accept != "" {
			hr.Header.Set("Accept", req.accept)
		} else {
			hr.Header.Set("Accept", "application/json")
		}
		if key != "" {
			hr.Header.Set("Idempotency-Key", key)
		}
		for k, v := range req.header {
			hr.Header.Set(k, v)
		}
		resp, err := c.http.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.log.Debug("instabox request failed", "method", req.method, "path", req.path, "attempt", attempt, "error", err)
			// A transport error may come after the server acted: only
			// safe calls (GET or with an Idempotency-Key) retry it.
			if req.retry == retrySafe && attempt < c.maxRetries {
				if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("instabox: %s %s: %w", req.method, req.path, err)
		}
		c.log.Debug("instabox request", "method", req.method, "path", req.path, "status", resp.StatusCode, "attempt", attempt)
		if resp.StatusCode < 400 {
			return resp, nil
		}
		apiErr := readAPIError(req.method, req.path, resp)
		wait, ok := c.retryWait(req.retry, apiErr, attempt)
		if !ok {
			return nil, apiErr
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// retryWait decides whether apiErr is retried and how long to wait.
func (c *Client) retryWait(mode retryMode, apiErr *APIError, attempt int) (time.Duration, bool) {
	if attempt >= c.maxRetries || mode == retryNever {
		return 0, false
	}
	retryable := apiErr.Retryable
	if apiErr.Code == "" {
		// No JSON error body (a proxy or a load balancer answered).
		switch apiErr.Status {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			retryable = true
		}
	}
	if !retryable {
		return 0, false
	}
	if mode == retryNotStarted {
		switch apiErr.Code {
		case CodeQuotaExceeded, CodeRateLimited, CodeConcurrencyLimit, CodeOperationConflict:
		default:
			return 0, false
		}
	}
	if apiErr.RetryAfter > 0 {
		if apiErr.RetryAfter > c.maxDelay {
			return 0, false
		}
		return apiErr.RetryAfter, true
	}
	return c.backoff(attempt), true
}

// backoff is exponential with up to 25% jitter, capped at maxDelay.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.baseDelay << min(attempt, 16)
	if d <= 0 || d > c.maxDelay {
		d = c.maxDelay
	}
	return d - time.Duration(mrand.Int64N(int64(d)/4+1))
}

func readAPIError(method, path string, resp *http.Response) *APIError {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &APIError{Method: method, Path: path, Status: resp.StatusCode}
	var body errorBody
	if json.Unmarshal(data, &body) == nil && body.Error != nil {
		e.ErrorDetail = *body.Error
	} else {
		e.Message = strings.TrimSpace(string(data))
		if len(e.Message) > 200 {
			e.Message = e.Message[:200]
		}
	}
	if e.RetryAfterSeconds > 0 {
		e.RetryAfter = time.Duration(e.RetryAfterSeconds) * time.Second
	} else if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}

// call sends a JSON request and decodes a JSON response into out (when
// out is not nil). It bounds the call with defaultCallTimeout unless ctx
// has an earlier deadline.
func (c *Client) call(ctx context.Context, req request, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	resp, err := c.do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		return fmt.Errorf("instabox: decode %s %s: %w", req.method, req.path, err)
	}
	return nil
}

func machinePath(id string, parts ...string) string {
	p := "/v1/machines/" + url.PathEscape(id)
	for _, s := range parts {
		p += "/" + s
	}
	return p
}
