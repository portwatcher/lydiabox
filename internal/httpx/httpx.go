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
)

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
