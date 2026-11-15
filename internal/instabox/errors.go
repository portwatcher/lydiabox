package instabox

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Error codes the client branches on (schema Error, enum code). The full
// list is at https://instabox.sh/docs/reference/errors.md.
const (
	CodeBadRequest                 = "bad_request"
	CodeUnknownField               = "unknown_field"
	CodeSpecNotAllowed             = "spec_not_allowed"
	CodeNotFound                   = "not_found"
	CodeMissingParent              = "missing_parent"
	CodePermissionDenied           = "permission_denied"
	CodeMissingScope               = "missing_scope"
	CodeTemplateNotAllowed         = "template_not_allowed"
	CodeOperationConflict          = "operation_conflict"
	CodeIdempotencyConflict        = "idempotency_conflict"
	CodeMachineNotRunning          = "machine_not_running"
	CodeDeleteConfirmationRequired = "delete_confirmation_required"
	CodeMachineCooldown            = "machine_cooldown"
	CodeNotAFile                   = "not_a_file"
	CodeTooLarge                   = "too_large"
	CodeQuotaExceeded              = "quota_exceeded"
	CodeRateLimited                = "rate_limited"
	CodeConcurrencyLimit           = "concurrency_limit"
	CodeNoCapacity                 = "no_capacity"
	CodeHostUnavailable            = "host_unavailable"
	CodeInternal                   = "internal"
	CodeNoSpace                    = "no_space"
	CodeCreditExhausted            = "credit_exhausted"
)

// Sentinel errors. An *APIError matches them with errors.Is.
var (
	// ErrUnauthorized: the API key is missing, malformed, unknown,
	// expired, or revoked (HTTP 401).
	ErrUnauthorized = errors.New("instabox: unauthorized")
	// ErrForbidden: the key lacks a scope or the plan forbids the request
	// (HTTP 403).
	ErrForbidden = errors.New("instabox: forbidden")
	// ErrPaymentRequired: a billing limit (HTTP 402).
	ErrPaymentRequired = errors.New("instabox: payment required")
	// ErrNotFound: no such machine, snapshot, port, or file (HTTP 404).
	ErrNotFound = errors.New("instabox: not found")
	// ErrConflict: another operation runs, or the change is not allowed
	// in the current state (409 operation_conflict).
	ErrConflict = errors.New("instabox: operation conflict")
	// ErrNotRunning: the request needs a running machine (409
	// machine_not_running).
	ErrNotRunning = errors.New("instabox: machine not running")
	// ErrIdempotencyConflict: the same Idempotency-Key on another request.
	ErrIdempotencyConflict = errors.New("instabox: idempotency conflict")
	// ErrQuota: a plan limit or a rate limit (HTTP 429).
	ErrQuota = errors.New("instabox: quota exceeded")
	// ErrNoCapacity: no host has room now (no_capacity).
	ErrNoCapacity = errors.New("instabox: no capacity")
	// ErrUnavailable: the machine's host or the service cannot be reached
	// (HTTP 5xx other than no_capacity).
	ErrUnavailable = errors.New("instabox: unavailable")
)

// ErrorDetail is the error object of the API (schema Error). It is also
// the error of a failed Operation and a machine's lastError.
type ErrorDetail struct {
	Code              string   `json:"code"`
	Message           string   `json:"message"`
	RequestID         string   `json:"requestId,omitempty"`
	Retryable         bool     `json:"retryable"`
	OperationID       *string  `json:"operationId,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	RetryAfterSeconds int      `json:"retryAfterSeconds,omitempty"`
	RequiredScopes    []string `json:"requiredScopes,omitempty"`
	DocsURL           string   `json:"docs_url,omitempty"`
}

type errorBody struct {
	Error *ErrorDetail `json:"error"`
}

// APIError is a failed API call or a failed operation. It never holds
// request headers, so it cannot carry the API key.
type APIError struct {
	Method string
	Path   string
	// Status is the HTTP status; 0 for a failed operation.
	Status int
	ErrorDetail
	// RetryAfter is the Retry-After header or retryAfterSeconds.
	RetryAfter time.Duration
	// Operation is set when an operation failed (WaitOperation).
	Operation *Operation
}

func (e *APIError) Error() string {
	code := e.Code
	if code == "" {
		code = http.StatusText(e.Status)
	}
	msg := e.Message
	if msg == "" {
		msg = "no message"
	}
	if e.Operation != nil {
		return fmt.Sprintf("instabox: operation %s (%s) failed: %s: %s", e.Operation.ID, e.Operation.Type, code, msg)
	}
	if e.RequestID != "" {
		return fmt.Sprintf("instabox: %s %s: HTTP %d %s: %s (request %s)", e.Method, e.Path, e.Status, code, msg, e.RequestID)
	}
	return fmt.Sprintf("instabox: %s %s: HTTP %d %s: %s", e.Method, e.Path, e.Status, code, msg)
}

// Is maps the error to the sentinel errors by code first and HTTP status
// second.
func (e *APIError) Is(target error) bool {
	switch e.Code {
	case CodeNoCapacity:
		return target == ErrNoCapacity
	case CodeOperationConflict:
		return target == ErrConflict
	case CodeMachineNotRunning:
		return target == ErrNotRunning
	case CodeIdempotencyConflict:
		return target == ErrIdempotencyConflict
	case CodeNotFound, CodeMissingParent:
		return target == ErrNotFound
	}
	switch {
	case e.Status == http.StatusUnauthorized:
		return target == ErrUnauthorized
	case e.Status == http.StatusForbidden:
		return target == ErrForbidden
	case e.Status == http.StatusPaymentRequired:
		return target == ErrPaymentRequired
	case e.Status == http.StatusNotFound:
		return target == ErrNotFound
	case e.Status == http.StatusTooManyRequests:
		return target == ErrQuota
	case e.Status >= 500 && e.Status != http.StatusInsufficientStorage:
		return target == ErrUnavailable
	}
	return false
}

// CodeOf returns the API error code of err, or "".
func CodeOf(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// IsRetryable reports whether the server said the same request may
// succeed later.
func IsRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}
