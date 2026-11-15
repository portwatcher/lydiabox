// Package instaboxtest is a fake InstaBox API for tests. It follows the
// shapes of the OpenAPI document that package instabox was written
// against (instabox.OpenAPISHA256): API key checks, Idempotency-Key replay
// and conflicts, machines with timed state transitions (creating,
// running, suspended, stopped, deleted), operations with long polls, wake
// of idle-paused machines on use, exec with server-sent events, files,
// private ports, snapshots, quota and not-found errors, and injected
// failures for retry tests. It keeps its own JSON types so that a shape
// mistake in the client is not copied into the fake.
package instaboxtest

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configures a Server.
type Options struct {
	// APIKey is the only accepted key (default "ibk_test").
	APIKey string
	// OpDelay is how long an operation runs before it ends (default
	// 20ms). Machines are in a transitional state meanwhile.
	OpDelay time.Duration
	// MaxMachines and MaxSnapshots are the plan limits (0: no limit).
	MaxMachines  int
	MaxSnapshots int
	// FreePlan accepts only an idle pause of 1800 s.
	FreePlan bool
	// Exec answers commands (default DefaultExec).
	Exec ExecFunc
}

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

type injected struct {
	method, prefix string
	status         int
	code           string
	retryable      bool
	retryAfter     int
	disconnect     bool
	count          int
}

type idemRecord struct {
	method, path, hash string
	status             int
	header             http.Header
	body               []byte
}

// Server is a running fake. Its methods are safe for concurrent use.
type Server struct {
	*httptest.Server
	opts Options

	mu          sync.Mutex
	machines    map[string]*machine
	ops         map[string]*operation
	snapshots   map[string]*snapshot
	idem        map[string]*idemRecord
	inject      []*injected
	requests    []Request
	replays     int
	failCreate  *createFailure
	execLateErr *lateError
}

// New starts a fake server; t.Cleanup(s.Close) stops it.
func New(opts Options) *Server {
	if opts.APIKey == "" {
		opts.APIKey = "ibk_test"
	}
	if opts.OpDelay <= 0 {
		opts.OpDelay = 20 * time.Millisecond
	}
	if opts.Exec == nil {
		opts.Exec = DefaultExec
	}
	s := &Server{
		opts:      opts,
		machines:  map[string]*machine{},
		ops:       map[string]*operation{},
		snapshots: map[string]*snapshot{},
		idem:      map[string]*idemRecord{},
	}
	s.Server = httptest.NewServer(s.handler())
	return s
}

// APIKey returns the accepted key.
func (s *Server) APIKey() string { return s.opts.APIKey }

// Requests returns a copy of the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Replays counts responses served from the idempotency store.
func (s *Server) Replays() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replays
}

// InjectError makes the next count requests whose method matches and
// whose path starts with prefix fail with status and code.
func (s *Server) InjectError(method, prefix string, status int, code string, retryable bool, retryAfter, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inject = append(s.inject, &injected{method: method, prefix: prefix, status: status, code: code,
		retryable: retryable, retryAfter: retryAfter, count: count})
}

// InjectDisconnect makes the next count matching requests reach the
// handler and then lose the connection before the answer (afterHandler)
// or before the handler runs (!afterHandler).
func (s *Server) InjectDisconnect(method, prefix string, afterHandler bool, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inject = append(s.inject, &injected{method: method, prefix: prefix, disconnect: true,
		retryable: afterHandler, count: count})
}

func (s *Server) takeInjection(r *http.Request) *injected {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range s.inject {
		if in.count > 0 && in.method == r.Method && strings.HasPrefix(r.URL.Path, in.prefix) {
			in.count--
			return in
		}
	}
	return nil
}

// apiErr writes the error envelope.
func apiErr(w http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	body := map[string]any{
		"code": code, "message": msg, "requestId": "req_" + randID(12),
		"retryable": false, "operationId": nil,
		"docs_url": "https://instabox.sh/docs/errors#" + strings.ReplaceAll(code, "_", "-"),
	}
	for k, v := range extra {
		body[k] = v
	}
	if ra, ok := body["retryAfterSeconds"].(int); ok && ra > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ra))
	}
	writeJSON(w, status, map[string]any{"error": body})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a JSON body into v, refusing unknown fields as the API
// does (400 unknown_field). An empty body is valid when optional.
func decode(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	data, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if len(bytes.TrimSpace(data)) == 0 {
		if optional {
			return true
		}
		apiErr(w, http.StatusBadRequest, "bad_request", "a JSON body is required", nil)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			apiErr(w, http.StatusBadRequest, "unknown_field", err.Error(), nil)
		} else {
			apiErr(w, http.StatusBadRequest, "bad_request", err.Error(), nil)
		}
		return false
	}
	return true
}

// randID returns n lowercase base32 characters.
func randID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// authorized checks X-API-Key or Authorization: Bearer.
func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	key := r.Header.Get("X-API-Key")
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key != "" && bearer != "" && key != bearer {
		apiErr(w, http.StatusBadRequest, "bad_request", "X-API-Key and Authorization carry different keys", nil)
		return false
	}
	if key == "" {
		key = bearer
	}
	switch {
	case key == "":
		apiErr(w, http.StatusUnauthorized, "missing_key", "no API key", map[string]any{"reason": "missing_key"})
		return false
	case !strings.HasPrefix(key, "ibk_"):
		apiErr(w, http.StatusUnauthorized, "malformed_key", "malformed API key", map[string]any{"reason": "malformed_key"})
		return false
	case key != s.opts.APIKey:
		apiErr(w, http.StatusUnauthorized, "invalid_key", "no such API key", map[string]any{"reason": "invalid_key"})
		return false
	}
	return true
}

// recorder buffers a handler's answer for the idempotency store.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

// idempotent wraps a handler of a route that requires Idempotency-Key.
func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 200 {
			apiErr(w, http.StatusBadRequest, "bad_request", "Idempotency-Key header is required (1-200 printable ASCII)", nil)
			return
		}
		data, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		s.mu.Lock()
		rec, ok := s.idem[key]
		if ok {
			if rec.method != r.Method || rec.path != r.URL.Path || rec.hash != hash {
				s.mu.Unlock()
				apiErr(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was used for another request", nil)
				return
			}
			s.replays++
			s.mu.Unlock()
			for k, v := range rec.header {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body)
			return
		}
		s.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(data))
		rw := &recorder{header: http.Header{}}
		h(rw, r)
		if rw.status < 500 && rw.status != http.StatusTooManyRequests {
			s.mu.Lock()
			s.idem[key] = &idemRecord{method: r.Method, path: r.URL.Path, hash: hash, status: rw.status, header: rw.header.Clone(), body: rw.body.Bytes()}
			s.mu.Unlock()
		}
		for k, v := range rw.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rw.status)
		_, _ = w.Write(rw.body.Bytes())
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	idem := s.idempotent
	mux.HandleFunc("POST /v1/machines", idem(s.createMachine))
	mux.HandleFunc("GET /v1/machines", s.listMachines)
	mux.HandleFunc("GET /v1/machines/{id}", s.getMachine)
	mux.HandleFunc("PATCH /v1/machines/{id}", idem(s.updateMachine))
	mux.HandleFunc("DELETE /v1/machines/{id}", idem(s.deleteMachine))
	for _, action := range []string{"start", "stop", "suspend", "resume"} {
		mux.HandleFunc("POST /v1/machines/{id}/"+action, idem(s.lifecycle(action)))
	}
	mux.HandleFunc("POST /v1/machines/{id}/wake", s.wakeMachine)
	mux.HandleFunc("GET /v1/operations/{id}", s.getOperation)
	mux.HandleFunc("PUT /v1/machines/{id}/activity-leases/{lease}", s.putLease)
	mux.HandleFunc("DELETE /v1/machines/{id}/activity-leases/{lease}", s.deleteLease)
	mux.HandleFunc("POST /v1/machines/{id}/exec", s.exec)
	mux.HandleFunc("PUT /v1/machines/{id}/files", s.putFile)
	mux.HandleFunc("GET /v1/machines/{id}/files", s.statFile)
	mux.HandleFunc("GET /v1/machines/{id}/files/content", s.getFile)
	mux.HandleFunc("POST /v1/machines/{id}/ports", s.publishPort)
	mux.HandleFunc("GET /v1/machines/{id}/ports", s.listPorts)
	mux.HandleFunc("DELETE /v1/machines/{id}/ports/{port}", s.unpublishPort)
	mux.HandleFunc("POST /v1/machines/{id}/snapshots", idem(s.createSnapshot))
	mux.HandleFunc("GET /v1/snapshots", s.listSnapshots)
	mux.HandleFunc("GET /v1/snapshots/{id}", s.getSnapshot)
	mux.HandleFunc("DELETE /v1/snapshots/{id}", idem(s.deleteSnapshot))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
		s.mu.Unlock()
		if !s.authorized(w, r) {
			return
		}
		if in := s.takeInjection(r); in != nil {
			if in.disconnect {
				if in.retryable {
					mux.ServeHTTP(&recorder{header: http.Header{}}, r)
				}
				hijackClose(w)
				return
			}
			extra := map[string]any{"retryable": in.retryable}
			if in.retryAfter > 0 {
				extra["retryAfterSeconds"] = in.retryAfter
			}
			apiErr(w, in.status, in.code, fmt.Sprintf("injected %s", in.code), extra)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func hijackClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("instaboxtest: response writer cannot hijack")
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}
