package instaboxtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ExecRequest is the body of POST /v1/machines/{id}/exec (schema
// ExecRequest).
type ExecRequest struct {
	Cmd            string            `json:"cmd"`
	Argv           []string          `json:"argv"`
	Cwd            string            `json:"cwd"`
	Envs           map[string]string `json:"envs"`
	Stdin          string            `json:"stdin"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
	NoHistory      bool              `json:"noHistory"`
	HistoryCommand string            `json:"historyCommand"`
}

// ExecResult is the answer of an exec (schema ExecResult).
type ExecResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exitCode"`
	TimedOut  bool   `json:"timedOut"`
	Truncated bool   `json:"truncated"`
}

// ExecFunc answers a command run in machineID.
type ExecFunc func(machineID string, req ExecRequest) ExecResult

// DefaultExec answers "echo ARGS" (argv or cmd) with ARGS and a newline
// on stdout, "exit N" with exit code N, "cat" with stdin, and anything
// else with exit code 0 and no output.
func DefaultExec(_ string, req ExecRequest) ExecResult {
	args := req.Argv
	if req.Cmd != "" {
		args = strings.Fields(req.Cmd)
	}
	switch {
	case len(args) > 0 && args[0] == "echo":
		return ExecResult{Stdout: strings.Join(args[1:], " ") + "\n"}
	case len(args) == 2 && args[0] == "exit":
		n, _ := strconv.Atoi(args[1])
		return ExecResult{ExitCode: n, Stderr: "exit " + args[1] + "\n"}
	case len(args) == 1 && args[0] == "cat":
		return ExecResult{Stdout: req.Stdin}
	}
	return ExecResult{}
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var req ExecRequest
	if !decode(w, r, &req, false) {
		return
	}
	if (req.Cmd == "") == (len(req.Argv) == 0) {
		apiErr(w, http.StatusBadRequest, "bad_request", "set exactly one of cmd and argv", nil)
		return
	}
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > 600 {
		apiErr(w, http.StatusBadRequest, "bad_request", "timeoutSeconds must be 0-600", nil)
		return
	}
	s.mu.Lock()
	m := s.useMachine(w, r.PathValue("id"))
	s.mu.Unlock()
	if m == nil {
		return
	}
	res := s.opts.Exec(m.id, req)
	s.mu.Lock()
	late := s.execLateErr
	s.execLateErr = nil
	s.mu.Unlock()
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if r.Header.Get("X-Instabox-Exec-Keepalive") != "1" {
			writeJSON(w, http.StatusOK, res)
			return
		}
		// Keepalive form: 200, newlines while the command runs, then the
		// result or {"error", "status"}.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "\n\n")
		if late != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": late.body, "status": late.status})
			return
		}
		_ = json.NewEncoder(w).Encode(res)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	event := func(name string, v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = io.WriteString(w, ": ping\n\n")
	for _, line := range strings.SplitAfter(res.Stdout, "\n") {
		if line != "" {
			event("stdout", map[string]string{"data": line})
		}
	}
	for _, line := range strings.SplitAfter(res.Stderr, "\n") {
		if line != "" {
			event("stderr", map[string]string{"data": line})
		}
	}
	res.Stdout, res.Stderr, res.Truncated = "", "", false
	event("exit", res)
}

type lateError struct {
	status int
	body   map[string]any
}

// FailNextExecAfterStart makes the next exec that sends the keepalive
// header answer 200 and then fail with code and status, as an exec does
// when its machine is lost while the command runs.
func (s *Server) FailNextExecAfterStart(code string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execLateErr = &lateError{status: status, body: map[string]any{"code": code, "message": "injected " + code,
		"requestId": "req_" + randID(12), "retryable": false, "operationId": nil}}
}

type file struct {
	data    []byte
	mode    uint32
	modTime time.Time
}

var modeRE = regexp.MustCompile(`^0?[0-7]{3}$`)

func (s *Server) putFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := q.Get("path")
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || len(p) > 4096 {
		apiErr(w, http.StatusBadRequest, "bad_request", "path must be absolute and name a file", nil)
		return
	}
	p = path.Clean(p)
	var mode uint32
	if v := q.Get("mode"); v != "" {
		if !modeRE.MatchString(v) {
			apiErr(w, http.StatusBadRequest, "bad_request", "invalid mode", nil)
			return
		}
		n, _ := strconv.ParseUint(v, 8, 32)
		mode = uint32(n)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		apiErr(w, http.StatusBadRequest, "bad_request", "upload cut off", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.useMachine(w, r.PathValue("id"))
	if m == nil {
		return
	}
	if m.dirs[p] {
		apiErr(w, http.StatusConflict, "not_a_file", p+" is a directory", nil)
		return
	}
	dir := path.Dir(p)
	if !m.dirs[dir] {
		if q.Get("parents") != "true" {
			apiErr(w, http.StatusNotFound, "missing_parent", dir+" does not exist", nil)
			return
		}
		for d := dir; d != "/" && !m.dirs[d]; d = path.Dir(d) {
			m.dirs[d] = true
		}
	}
	old, existed := m.files[p]
	if mode == 0 {
		mode = 0o644
		if existed {
			mode = old.mode
		}
	}
	m.files[p] = &file{data: data, mode: mode, modTime: time.Now().UTC()}
	sum := sha256.Sum256(data)
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"path": p, "size": len(data), "sha256": hex.EncodeToString(sum[:]),
		"mode": fmt.Sprintf("%04o", mode), "created": !existed})
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	p := path.Clean(r.URL.Query().Get("path"))
	s.mu.Lock()
	m := s.useMachine(w, r.PathValue("id"))
	if m == nil {
		s.mu.Unlock()
		return
	}
	f, ok := m.files[p]
	isDir := m.dirs[p]
	s.mu.Unlock()
	switch {
	case isDir:
		apiErr(w, http.StatusConflict, "not_a_file", p+" is a directory", nil)
		return
	case !ok:
		notFound(w, "file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(f.data)))
	w.Header().Set("X-Instabox-File-Mode", fmt.Sprintf("%04o", f.mode))
	w.Header().Set("ETag", fmt.Sprintf(`"%d-%d"`, len(f.data), f.modTime.UnixNano()))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.data)
}

func modeString(dir bool, mode uint32) string {
	const rwx = "rwxrwxrwx"
	b := []byte("-")
	if dir {
		b[0] = 'd'
	}
	for i := 0; i < 9; i++ {
		if mode&(1<<(8-i)) != 0 {
			b = append(b, rwx[i])
		} else {
			b = append(b, '-')
		}
	}
	return string(b)
}

func (s *Server) statFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	p = trimSlash(path.Clean(p))
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.useMachine(w, r.PathValue("id"))
	if m == nil {
		return
	}
	if f, ok := m.files[p]; ok {
		out := map[string]any{"path": p, "type": "file", "size": len(f.data), "mode": modeString(false, f.mode), "modTime": f.modTime}
		if utf8.Valid(f.data) {
			out["content"] = string(f.data)
		} else {
			out["binary"] = true
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !m.dirs[p] {
		notFound(w, "path")
		return
	}
	entries := []map[string]any{}
	var dirs, files []string
	for d := range m.dirs {
		if d != p && path.Dir(d) == p {
			dirs = append(dirs, d)
		}
	}
	for f := range m.files {
		if path.Dir(f) == p {
			files = append(files, f)
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	for _, d := range dirs {
		entries = append(entries, map[string]any{"name": path.Base(d), "type": "dir", "size": 0, "mode": modeString(true, 0o755), "modTime": m.createdAt})
	}
	for _, f := range files {
		fl := m.files[f]
		entries = append(entries, map[string]any{"name": path.Base(f), "type": "file", "size": len(fl.data), "mode": modeString(false, fl.mode), "modTime": fl.modTime})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "type": "dir", "size": 0, "mode": modeString(true, 0o755), "modTime": m.createdAt, "entries": entries})
}

// File returns a file of a machine as written through the API.
func (s *Server) File(id, p string) ([]byte, uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lookup(id)
	if m == nil {
		return nil, 0, false
	}
	f, ok := m.files[path.Clean(p)]
	if !ok {
		return nil, 0, false
	}
	return append([]byte(nil), f.data...), f.mode, true
}

// RenameFile moves a file of a machine, as `mv -f from to` in the machine
// does. It reports whether from existed.
func (s *Server) RenameFile(id, from, to string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lookup(id)
	if m == nil {
		return false
	}
	f, ok := m.files[path.Clean(from)]
	if !ok {
		return false
	}
	delete(m.files, path.Clean(from))
	m.files[path.Clean(to)] = f
	return true
}

// RemoveFile removes a file of a machine, as `rm -f` in the machine does.
// It reports whether the file existed.
func (s *Server) RemoveFile(id, p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lookup(id)
	if m == nil {
		return false
	}
	_, ok := m.files[path.Clean(p)]
	delete(m.files, path.Clean(p))
	return ok
}
