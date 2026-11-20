package instaboxtest

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type updateReq struct {
	Name                    string            `json:"name"`
	AutoSuspendAfterSeconds *int              `json:"autoSuspendAfterSeconds"`
	MaxRunMinutes           *int              `json:"maxRunMinutes"`
	VCPUs                   int               `json:"vcpus"`
	MemMiB                  int               `json:"memMiB"`
	PersistentDiskGiB       int               `json:"persistentDiskGiB"`
	Metadata                map[string]string `json:"metadata"`
	Apply                   string            `json:"apply"`
}

func (s *Server) updateMachine(w http.ResponseWriter, r *http.Request) {
	var req updateReq
	if !decode(w, r, &req, false) {
		return
	}
	if !validIdle(w, req.AutoSuspendAfterSeconds, s.opts.FreePlan) {
		return
	}
	if req.MaxRunMinutes != nil && *req.MaxRunMinutes < -1 {
		apiErr(w, http.StatusBadRequest, "bad_request", "maxRunMinutes must be -1 or more", nil)
		return
	}
	if req.MaxRunMinutes != nil && s.opts.FreePlan {
		apiErr(w, http.StatusForbidden, "permission_denied", "the free plan sets the maximum run", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	m := s.lookup(r.PathValue("id"))
	if m == nil {
		notFound(w, "sandbox")
		return
	}
	if req.Name != "" {
		if !nameRE.MatchString(req.Name) {
			apiErr(w, http.StatusBadRequest, "bad_request", "invalid name", nil)
			return
		}
		if other := s.lookup(req.Name); other != nil && other != m {
			apiErr(w, http.StatusConflict, "operation_conflict", "name in use", nil)
			return
		}
		m.name = req.Name
	}
	if req.AutoSuspendAfterSeconds != nil {
		v := *req.AutoSuspendAfterSeconds
		if v == 0 {
			m.autoSuspend = nil
		} else {
			m.autoSuspend = &v
		}
	}
	if req.MaxRunMinutes != nil {
		m.maxRun = *req.MaxRunMinutes
	}
	if req.Metadata != nil {
		m.metadata = req.Metadata
	}
	if req.VCPUs != 0 || req.MemMiB != 0 {
		if m.state != "stopped" && req.Apply != "restart" {
			apiErr(w, http.StatusConflict, "operation_conflict", "stop the sandbox to resize it", nil)
			return
		}
		m.vcpus, m.memMiB = req.VCPUs, req.MemMiB
		if req.Apply == "restart" && m.state != "stopped" {
			op := s.startOp(m, "resize", "stopping", "running")
			writeJSON(w, http.StatusAccepted, op.json(m))
			return
		}
	}
	m.updatedAt = time.Now().UTC()
	writeJSON(w, http.StatusOK, m.json())
}

func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	id := r.PathValue("id")
	m := s.lookup(id)
	if m == nil {
		notFound(w, "sandbox")
		return
	}
	confirm := r.Header.Values("X-Confirm-Delete")
	if len(confirm) != 1 || (confirm[0] != m.id && confirm[0] != m.name) {
		apiErr(w, http.StatusConflict, "delete_confirmation_required", "send X-Confirm-Delete: "+m.id, nil)
		return
	}
	if m.opID != "" {
		if op := s.ops[m.opID]; op.typ == "delete" {
			writeJSON(w, http.StatusAccepted, op.json(m))
			return
		}
		apiErr(w, http.StatusConflict, "operation_conflict", "an operation is running", map[string]any{"operationId": m.opID})
		return
	}
	op := s.startOp(m, "delete", "deleting", "deleted")
	writeJSON(w, http.StatusAccepted, op.json(m))
}

type startReq struct {
	VCPUs  int `json:"vcpus"`
	MemMiB int `json:"memMiB"`
}

type stopReq struct {
	TimeoutSeconds int  `json:"timeoutSeconds"`
	Now            bool `json:"now"`
}

type emptyReq struct{}

// lifecycle handles start, stop, suspend, and resume.
func (s *Server) lifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		switch action {
		case "start":
			ok = decode(w, r, &startReq{}, true)
		case "stop":
			ok = decode(w, r, &stopReq{}, true)
		default:
			ok = decode(w, r, &emptyReq{}, true)
		}
		if !ok {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.advance()
		m := s.lookup(r.PathValue("id"))
		if m == nil {
			notFound(w, "sandbox")
			return
		}
		if m.opID != "" {
			op := s.ops[m.opID]
			if op.typ == action {
				writeJSON(w, http.StatusAccepted, op.json(m))
				return
			}
			apiErr(w, http.StatusConflict, "operation_conflict", op.typ+" is running", map[string]any{"operationId": op.id})
			return
		}
		var op *operation
		switch action + "/" + m.state {
		case "start/running", "stop/stopped", "suspend/suspended", "resume/running":
			op = s.doneOp(m, action)
		case "start/stopped", "start/error":
			op = s.startOp(m, "start", "starting", "running")
		case "start/suspended", "resume/suspended":
			m.autoSuspendedAt = nil
			op = s.startOp(m, action, "resuming", "running")
			if s.opts.ResumeDelay > 0 {
				op.doneAt = op.createdAt.Add(s.opts.ResumeDelay)
			}
		case "stop/running":
			op = s.startOp(m, "stop", "stopping", "stopped")
		case "suspend/running":
			m.autoSuspendedAt = nil
			if s.opts.SuspendDelay > 0 {
				// Like the real service during a slow suspend: the machine
				// shows "running" with desiredState "suspended" and the
				// operation until the suspend ends.
				op = s.startOp(m, "suspend", "running", "suspended")
				op.doneAt = op.createdAt.Add(s.opts.SuspendDelay)
			} else {
				op = s.startOp(m, "suspend", "suspending", "suspended")
			}
		case "stop/suspended":
			apiErr(w, http.StatusConflict, "operation_conflict", "resume it first, then stop it, or delete it", nil)
			return
		default:
			apiErr(w, http.StatusConflict, "operation_conflict", "cannot "+action+" a "+m.state+" sandbox", nil)
			return
		}
		switch action {
		case "stop":
			m.desiredState = "stopped"
		case "suspend":
			m.desiredState = "suspended"
		default:
			m.desiredState = "running"
		}
		writeJSON(w, http.StatusAccepted, op.json(m))
	}
}

// useMachine returns a running machine for exec, files, and leases. An
// idle-paused machine is resumed first (trigger "request"); another
// machine that is not running is 409 machine_not_running. Callers hold
// s.mu.
func (s *Server) useMachine(w http.ResponseWriter, id string) *machine {
	s.advance()
	m := s.lookup(id)
	if m == nil {
		notFound(w, "sandbox")
		return nil
	}
	if m.state == "suspended" && m.autoSuspendedAt != nil {
		s.wakeLocked(m, "request")
	}
	if m.state != "running" {
		apiErr(w, http.StatusConflict, "machine_not_running", "sandbox is "+m.state+"; start it first", nil)
		return nil
	}
	return m
}

func (s *Server) wakeMachine(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &emptyReq{}, true) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.useMachine(w, r.PathValue("id"))
	if m == nil {
		return
	}
	writeJSON(w, http.StatusOK, m.json())
}

func (s *Server) getOperation(w http.ResponseWriter, r *http.Request) {
	wait := time.Duration(0)
	if v := r.URL.Query().Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			n, err2 := strconv.Atoi(v)
			if err2 != nil {
				apiErr(w, http.StatusBadRequest, "bad_request", "invalid wait", nil)
				return
			}
			d = time.Duration(n) * time.Second
		}
		wait = min(d, 30*time.Second)
	}
	deadline := time.Now().Add(wait)
	id := r.PathValue("id")
	first := ""
	for {
		s.mu.Lock()
		s.advance()
		o, ok := s.ops[id]
		if !ok {
			s.mu.Unlock()
			notFound(w, "operation")
			return
		}
		if first == "" {
			first = o.state
		}
		if o.state == "succeeded" || o.state == "failed" || o.state != first || !time.Now().Before(deadline) {
			body := o.json(s.machines[o.machineID])
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, body)
			return
		}
		s.mu.Unlock()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *Server) putLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TTLSeconds int `json:"ttlSeconds"`
	}
	if !decode(w, r, &req, false) {
		return
	}
	if req.TTLSeconds == 0 {
		req.TTLSeconds = 300
	}
	if req.TTLSeconds < 30 || req.TTLSeconds > 3600 {
		apiErr(w, http.StatusBadRequest, "bad_request", "ttlSeconds must be 30-3600", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.useMachine(w, r.PathValue("id"))
	if m == nil {
		return
	}
	lease := r.PathValue("lease")
	if _, ok := m.leases[lease]; !ok && len(m.leases) >= 128 {
		apiErr(w, http.StatusTooManyRequests, "quota_exceeded", "at most 128 leases", nil)
		return
	}
	exp := time.Now().UTC().Add(time.Duration(req.TTLSeconds) * time.Second)
	m.leases[lease] = exp
	writeJSON(w, http.StatusOK, map[string]any{"id": lease, "machineId": m.id, "expiresAt": exp})
}

func (s *Server) deleteLease(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	m := s.lookup(r.PathValue("id"))
	if m == nil {
		notFound(w, "sandbox")
		return
	}
	delete(m.leases, r.PathValue("lease"))
	w.WriteHeader(http.StatusNoContent)
}

// Leases returns the lease IDs held on a machine.
func (s *Server) Leases(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lookup(id)
	if m == nil {
		return nil
	}
	var out []string
	for k := range m.leases {
		out = append(out, k)
	}
	return out
}

// trimSlash removes a trailing slash except from "/".
func trimSlash(p string) string {
	if len(p) > 1 {
		return strings.TrimRight(p, "/")
	}
	return p
}
