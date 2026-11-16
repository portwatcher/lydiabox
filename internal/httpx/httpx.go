// Package httpx holds the JSON conventions shared by every LydiaBox HTTP
// handler: response encoding, the error envelope, and bounded body decoding.
// See docs/api.md ("Conventions").
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Error codes used in the error envelope.
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeTooLarge     = "too_large"
	CodeUnsupported  = "unsupported_media_type"
	CodeUnavailable  = "unavailable"
	CodeInternal     = "internal"
	CodeNotImpl      = "not_implemented"
	CodeCanceled     = "canceled"
)

// StatusClientClosedRequest (499, a common extension) is the status of a
// request whose client went away before the answer: not a server error.
const StatusClientClosedRequest = 499

// ErrorBody is the JSON error envelope: {"error":{"code":"...","message":"..."}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one error.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError writes the error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// DefaultMaxBody bounds JSON request bodies (1 MiB).
const DefaultMaxBody = 1 << 20

// DecodeJSON decodes a JSON request body of at most maxBytes (DefaultMaxBody
// when maxBytes <= 0) into v, rejecting unknown fields and trailing data.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBody
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return fmt.Errorf("request body exceeds %d bytes", maxBytes)
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: unexpected trailing data")
	}
	return nil
}

// CauseNoter is implemented by the API's response recorder: it takes the
// cause of a 5xx response that the handler does not send to the client,
// so that the request log line has it.
type CauseNoter interface {
	NoteCause(err error)
}

// ExpectedNoter is implemented by the API's response recorder: it marks a
// 5xx response as an expected state (for example "VM is not running"), not
// a fault, so that the request log does not log it at the error level.
type ExpectedNoter interface {
	NoteExpected()
}

// NoteExpected marks the response of w as an expected state through the
// first ExpectedNoter in the chain of w (through Unwrap). Without one it
// does nothing.
func NoteExpected(w http.ResponseWriter) {
	for w != nil {
		if n, ok := w.(ExpectedNoter); ok {
			n.NoteExpected()
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// NoteCause gives err to the first CauseNoter in the chain of w (through
// Unwrap). Without one it does nothing.
func NoteCause(w http.ResponseWriter, err error) {
	for w != nil {
		if n, ok := w.(CauseNoter); ok {
			n.NoteCause(err)
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}
