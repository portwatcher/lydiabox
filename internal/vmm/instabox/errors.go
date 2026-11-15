package instabox

import (
	"errors"
	"fmt"
	"sync"
	"time"

	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// ErrMachineGone is returned when the recorded machine of a tenant no
// longer exists at InstaBox. The driver never makes a new machine in its
// place, because the files of the old one are lost: an operator decides
// with `lydiabox instabox status` and `lydiabox instabox destroy`.
var ErrMachineGone = errors.New("the InstaBox machine of this tenant was deleted; its files are lost. " +
	"Run 'lydiabox instabox status', then 'lydiabox instabox destroy -forget' to make a new machine")

// AccountError is an InstaBox refusal that a retry does not fix soon: a
// bad API key, a missing scope, a billing limit, a quota, or no capacity.
// The driver does not call the API again for the same tenant until After.
type AccountError struct {
	// Reason is a short, clear text for the API and the web status bar.
	Reason string
	// Code is the InstaBox error code.
	Code string
	// After is the earliest time of the next attempt.
	After time.Time
	err   error
}

func (e *AccountError) Error() string {
	return fmt.Sprintf("%s (InstaBox %s)", e.Reason, e.Code)
}

// Unwrap returns the API error and vmm.ErrUnavailable, so that the API
// answers 503.
func (e *AccountError) Unwrap() []error { return []error{e.err, vmm.ErrUnavailable} }

// DefaultBlockTime is how long an account error stops new attempts.
const DefaultBlockTime = 30 * time.Second

// classify turns an API error into an *AccountError when it is one; any
// other error is returned as it is. A not-found machine is ErrMachineGone.
func classify(err error, now time.Time, block time.Duration) error {
	if err == nil {
		return nil
	}
	code := ib.CodeOf(err)
	if code == "" {
		code = "error"
	}
	var apiErr *ib.APIError
	wait := block
	if errors.As(err, &apiErr) && apiErr.RetryAfter > wait {
		wait = apiErr.RetryAfter
	}
	reason := ""
	switch {
	case errors.Is(err, ib.ErrUnauthorized):
		reason = "InstaBox refused the API key: check LYDIABOX_INSTABOX_API_KEY"
	case code == ib.CodeCreditExhausted || errors.Is(err, ib.ErrPaymentRequired):
		reason = "InstaBox needs payment: the account has no credit or reached a billing limit"
	case errors.Is(err, ib.ErrForbidden):
		reason = "InstaBox refused the request: the API key lacks a scope (machines:read, machines:write) or the plan does not allow it"
	case errors.Is(err, ib.ErrNoCapacity):
		reason = "InstaBox has no capacity for the machine now; it retries later"
	case errors.Is(err, ib.ErrQuota):
		reason = "an InstaBox plan limit or rate limit was reached"
	case errors.Is(err, ib.ErrNotFound):
		return fmt.Errorf("%w: %w", vmm.ErrUnavailable, ErrMachineGone)
	default:
		return err
	}
	if apiErr != nil && apiErr.Message != "" {
		reason += ": " + apiErr.Message
	}
	return &AccountError{Reason: reason, Code: code, After: now.Add(wait), err: err}
}

// gate stops calls for a tenant after an account error, so that requests
// that wake the machine do not repeat a call that fails.
type gate struct {
	mu      sync.Mutex
	blocked map[string]*AccountError
}

func (g *gate) check(tid string, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.blocked[tid]
	if e == nil {
		return nil
	}
	if !now.Before(e.After) {
		delete(g.blocked, tid)
		return nil
	}
	return e
}

// record keeps err when it is an account error and clears the gate after
// a success (err == nil).
func (g *gate) record(tid string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var acc *AccountError
	switch {
	case err == nil:
		delete(g.blocked, tid)
	case errors.As(err, &acc):
		if g.blocked == nil {
			g.blocked = map[string]*AccountError{}
		}
		g.blocked[tid] = acc
	}
}
