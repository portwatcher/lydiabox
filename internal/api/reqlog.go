package api

import (
	"bytes"
	"encoding/json"

	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
)

// maxCauseBody is the part of a 5xx response body that the request log
// keeps to find the cause (the error envelope is much smaller).
const maxCauseBody = 4 << 10

// maxCauseLen bounds the cause in one log line.
const maxCauseLen = 512

// statusRecorder records the status, the size, the route, and for a 5xx
// response the start of the body and the cause that the handler noted.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	route  string
	cause  error
	body   bytes.Buffer
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 500 && r.body.Len() < maxCauseBody {
		r.body.Write(b[:min(len(b), maxCauseBody-r.body.Len())])
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach Flush and Hijack (WebSockets).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// NoteCause implements httpx.CauseNoter: a handler that hides the cause
// of a 5xx from the client gives it to the request log.
func (r *statusRecorder) NoteCause(err error) {
	if r.cause == nil {
		r.cause = err
	}
}

func (r *statusRecorder) setRoute(pattern string) { r.route = pattern }

// withRoute tells the request log the ServeMux pattern of the request.
// The mux sets Request.Pattern only on its own copy of the request.
func withRoute(pattern string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for cur := w; cur != nil; {
			if rec, ok := cur.(*statusRecorder); ok {
				rec.setRoute(pattern)
				break
			}
			u, ok := cur.(interface{ Unwrap() http.ResponseWriter })
			if !ok {
				break
			}
			cur = u.Unwrap()
		}
		h.ServeHTTP(w, r)
	})
}

// logRequests writes one line per request: at debug level, or for a 5xx
// response at error level with the route and the cause (the cause that the
// handler noted, else the message of the error envelope), without secrets.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		attrs := []any{"method", r.Method, "route", rec.route, "path", r.URL.Path,
			"status", rec.status, "bytes", rec.bytes, "duration", time.Since(start)}
		if rec.status < 500 {
			s.log.Debug("request", attrs...)
			return
		}
		attrs = append(attrs, "cause", redactCause(rec.causeText()))
		if r.Context().Err() != nil {
			attrs = append(attrs, "client_gone", true)
		}
		s.log.Error("request failed", attrs...)
	})
}

// causeText is the noted cause, else the error envelope's message, else
// the start of the body.
func (r *statusRecorder) causeText() string {
	if r.cause != nil {
		return r.cause.Error()
	}
	var env httpx.ErrorBody
	if err := json.Unmarshal(r.body.Bytes(), &env); err == nil && env.Error.Message != "" {
		return env.Error.Code + ": " + env.Error.Message
	}
	if t := strings.TrimSpace(r.body.String()); t != "" {
		return t
	}
	return "no cause recorded"
}

var (
	redactBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	redactParam  = regexp.MustCompile(`(?i)\b([a-z0-9_-]*(?:token|secret|key|password|passwd|signature|credential|auth)[a-z0-9_-]*)(["']?\s*[=:]\s*["']?)[^\s&"',;]+`)
	redactUser   = regexp.MustCompile(`(://)[^/@\s:]+:[^/@\s]+@`)
)

// redactCause removes credentials (Authorization values, key=value pairs
// whose key names a token, key, secret, or password, and URL user
// information) from a cause and bounds its length.
func redactCause(s string) string {
	s = redactBearer.ReplaceAllString(s, "$1 [redacted]")
	s = redactParam.ReplaceAllString(s, "$1$2[redacted]")
	s = redactUser.ReplaceAllString(s, "$1[redacted]@")
	if len(s) > maxCauseLen {
		s = s[:maxCauseLen] + "..."
	}
	return s
}
