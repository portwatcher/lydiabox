package instaboxtest

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Templates the fake accepts.
var templates = map[string]bool{"agent-browser": true, "universal": true, "ci": true, "android": true, "omarchy": true}

type machine struct {
	id, name, template, snapshotID string
	state, desiredState            string
	revision                       int64
	opID                           string
	vcpus, memMiB                  int
	autoSuspend                    *int
	maxRun                         int
	autoSuspendedAt                *time.Time
	runStartedAt                   *time.Time
	metadata                       map[string]string
	createdAt, updatedAt           time.Time
	files                          map[string]*file
	dirs                           map[string]bool
	ports                          map[int]*port
	leases                         map[string]time.Time
	// preserved is runtime.processesPreserved after a resume; freshResume
	// makes the next resume boot the machine fresh (FreshResumeNext).
	preserved   *bool
	freshResume bool
}

type operation struct {
	id, machineID, typ, state, trigger string
	errBody                            map[string]any
	final                              string
	doneAt, createdAt, updatedAt       time.Time
}

type createFailure struct {
	code       string
	retryable  bool
	retryAfter int
}

func (s *Server) newMachine(name, template, snapshotID string) *machine {
	now := time.Now().UTC()
	m := &machine{
		id: "m_" + randID(26), name: name, template: template, snapshotID: snapshotID,
		vcpus: 4, memMiB: 8192, maxRun: -1, metadata: map[string]string{},
		createdAt: now, updatedAt: now,
		files: map[string]*file{}, dirs: map[string]bool{"/": true, "/data": true, "/data/home": true, "/tmp": true},
		ports: map[int]*port{}, leases: map[string]time.Time{},
	}
	if m.name == "" {
		m.name = "box-" + randHex(4)
	}
	idle := 1800
	m.autoSuspend = &idle
	if s.opts.FreePlan {
		m.maxRun = 120
	}
	return m
}

func (m *machine) json() map[string]any {
	transitional := map[string]bool{"creating": true, "starting": true, "stopping": true, "suspending": true, "resuming": true, "deleting": true}
	var opID any
	if m.opID != "" {
		opID = m.opID
	}
	var autoSuspend any
	if m.autoSuspend != nil {
		autoSuspend = *m.autoSuspend
	}
	out := map[string]any{
		"id": m.id, "name": m.name, "state": m.state, "desiredState": m.desiredState,
		"revision": m.revision, "operationId": opID,
		"template":                map[string]any{"id": m.template, "version": "2026-11-01.1", "digest": "sha256:" + strings.Repeat("0", 64)},
		"spec":                    map[string]any{"vcpus": m.vcpus, "memMiB": m.memMiB, "persistentDiskGiB": 20},
		"storage":                 map[string]any{"sizeGiB": 20, "mountPath": "/data", "persistent": true, "synced": !transitional[m.state]},
		"runtime":                 m.runtimeJSON(),
		"autoSuspendAfterSeconds": autoSuspend, "maxRunMinutes": m.maxRun,
		"runStartedAt": m.runStartedAt, "cooldownUntil": nil,
		"metadata": m.metadata, "vault": false, "guestUser": "user", "orgId": "org_fake",
		"createdAt": m.createdAt, "updatedAt": m.updatedAt,
	}
	if m.autoSuspendedAt != nil {
		out["autoSuspendedAt"] = *m.autoSuspendedAt
	}
	if m.snapshotID != "" {
		out["snapshot"] = map[string]any{"id": m.snapshotID}
	}
	return out
}

func (m *machine) runtimeJSON() map[string]any {
	rt := map[string]any{"ready": m.state == "running"}
	if m.preserved != nil {
		rt["processesPreserved"] = *m.preserved
	}
	return rt
}

func (o *operation) json(m *machine) map[string]any {
	var errBody any
	if o.errBody != nil {
		errBody = o.errBody
	}
	out := map[string]any{
		"id": o.id, "machineId": o.machineID, "type": o.typ, "state": o.state,
		"error": errBody, "createdAt": o.createdAt, "updatedAt": o.updatedAt,
	}
	if o.trigger != "" {
		out["trigger"] = o.trigger
	}
	if o.state == "succeeded" && m != nil && m.state != "deleted" {
		out["machine"] = m.json()
	}
	return out
}

// advance ends the operations whose time has come. Callers hold s.mu.
func (s *Server) advance() {
	now := time.Now().UTC()
	for _, o := range s.ops {
		if o.state != "running" || now.Before(o.doneAt) {
			continue
		}
		o.updatedAt = now
		m := s.machines[o.machineID]
		if o.errBody != nil {
			o.state = "failed"
			if m != nil {
				m.state = "error"
				m.opID = ""
				s.queueGuest(false, m.id)
			}
			continue
		}
		o.state = "succeeded"
		if m == nil {
			continue
		}
		m.opID = ""
		m.state = o.final
		m.updatedAt = now
		if o.final == "running" {
			m.runStartedAt = &now
			m.preserved = nil
			if o.typ == "create" || o.typ == "start" || o.typ == "resize" {
				s.queueGuest(true, m.id)
			}
			if o.typ == "resume" {
				// A resume keeps the memory, unless FreshResumeNext asked
				// for a resume that cannot bring the processes back.
				kept := !m.freshResume
				m.preserved, m.freshResume = &kept, false
				if !kept {
					s.queueGuest(false, m.id)
					s.queueGuest(true, m.id)
				}
			}
		}
		if o.final == "stopped" || o.final == "deleted" {
			s.queueGuest(false, m.id)
		}
		if o.final == "deleted" {
			delete(s.machines, m.id)
		}
	}
}

// startOp begins an operation on m that ends with m in state final.
// Callers hold s.mu.
func (s *Server) startOp(m *machine, typ, transitional, final string) *operation {
	now := time.Now().UTC()
	o := &operation{id: "op_" + randID(26), machineID: m.id, typ: typ, state: "running", final: final,
		doneAt: now.Add(s.opts.OpDelay), createdAt: now, updatedAt: now}
	s.ops[o.id] = o
	m.opID = o.id
	m.state = transitional
	m.revision++
	m.updatedAt = now
	switch typ {
	case "suspend", "stop", "delete", "resize":
		// An explicit lifecycle operation ends the activity leases, and the
		// connections through the port proxy close.
		m.leases = map[string]time.Time{}
		s.pauseConns(m.id)
	}
	return o
}

// doneOp records an operation that is already in effect.
func (s *Server) doneOp(m *machine, typ string) *operation {
	now := time.Now().UTC()
	o := &operation{id: "op_" + randID(26), machineID: m.id, typ: typ, state: "succeeded", final: m.state,
		doneAt: now, createdAt: now, updatedAt: now}
	s.ops[o.id] = o
	return o
}

// lookup finds a live machine by ID or name. Callers hold s.mu.
func (s *Server) lookup(id string) *machine {
	if m, ok := s.machines[id]; ok {
		return m
	}
	for _, m := range s.machines {
		if m.name == id {
			return m
		}
	}
	return nil
}

func notFound(w http.ResponseWriter, what string) {
	apiErr(w, http.StatusNotFound, "not_found", what+" not found", nil)
}

// IdlePause pauses a running machine as the idle check would (its
// autoSuspendedAt is set), so that the next use wakes it.
func (s *Server) IdlePause(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	m := s.lookup(id)
	if m == nil || m.state != "running" {
		return false
	}
	now := time.Now().UTC()
	m.state, m.autoSuspendedAt = "suspended", &now
	m.revision++
	s.pauseConns(m.id)
	return true
}

// FreshResumeNext makes the next resume of a machine boot it fresh, as
// InstaBox does when it cannot restore the processes: the programs start
// again and runtime.processesPreserved is false.
func (s *Server) FreshResumeNext(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.lookup(id)
	if m == nil {
		return false
	}
	m.freshResume = true
	return true
}

// State returns a machine's state ("" when it does not exist).
func (s *Server) State(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	if m := s.lookup(id); m != nil {
		return m.state
	}
	return ""
}

// FailNextCreate makes the next create operation fail with code (for
// example "no_capacity").
func (s *Server) FailNextCreate(code string, retryable bool, retryAfter int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCreate = &createFailure{code: code, retryable: retryable, retryAfter: retryAfter}
}

func validIdle(w http.ResponseWriter, v *int, free bool) bool {
	if v == nil {
		return true
	}
	if free && *v != 1800 {
		apiErr(w, http.StatusForbidden, "spec_not_allowed", "the free plan pauses after 1800 s", nil)
		return false
	}
	if *v != 0 && (*v < 300 || *v > 1800 || *v%60 != 0) {
		apiErr(w, http.StatusBadRequest, "bad_request", "autoSuspendAfterSeconds must be 300-1800 in whole minutes, or 0", nil)
		return false
	}
	return true
}

type createReq struct {
	Name                    string            `json:"name"`
	TemplateID              string            `json:"templateId"`
	TemplateVersion         string            `json:"templateVersion"`
	SnapshotID              string            `json:"snapshotId"`
	OrgID                   string            `json:"orgId"`
	VCPUs                   int               `json:"vcpus"`
	MemMiB                  int               `json:"memMiB"`
	GPU                     string            `json:"gpu"`
	DesiredState            string            `json:"desiredState"`
	AutoSuspendAfterSeconds *int              `json:"autoSuspendAfterSeconds"`
	MaxRunMinutes           *int              `json:"maxRunMinutes"`
	DeleteAfterSeconds      int               `json:"deleteAfterSeconds"`
	ArchiveSeconds          int               `json:"archiveSeconds"`
	Env                     map[string]string `json:"env"`
	Metadata                map[string]string `json:"metadata"`
	GuestUser               string            `json:"guestUser"`
	FailFast                bool              `json:"failFast"`
	PersistentDiskGiB       *int              `json:"persistentDiskGiB"`
	RootDiskGiB             *int              `json:"rootDiskGiB"`
}

func (s *Server) createMachine(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !decode(w, r, &req, false) {
		return
	}
	if req.Name != "" && !nameRE.MatchString(req.Name) {
		apiErr(w, http.StatusBadRequest, "bad_request", "invalid name", nil)
		return
	}
	if req.TemplateID != "" && req.SnapshotID != "" {
		apiErr(w, http.StatusBadRequest, "bad_request", "templateId and snapshotId exclude each other", nil)
		return
	}
	if req.PersistentDiskGiB != nil && *req.PersistentDiskGiB != 20 {
		apiErr(w, http.StatusBadRequest, "spec_not_allowed", "the disk comes with the size: 20 GiB", nil)
		return
	}
	spec := strconv.Itoa(req.VCPUs) + "/" + strconv.Itoa(req.MemMiB)
	if req.VCPUs != 0 || req.MemMiB != 0 {
		allowed := map[string]bool{"4/8192": true}
		if !s.opts.FreePlan {
			allowed["2/4096"], allowed["8/16384"] = true, true
		}
		if !allowed[spec] {
			apiErr(w, http.StatusBadRequest, "spec_not_allowed", "size "+spec+" is not one of your plan's", nil)
			return
		}
	}
	if !validIdle(w, req.AutoSuspendAfterSeconds, s.opts.FreePlan) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	tmpl := req.TemplateID
	if req.SnapshotID != "" {
		snap, ok := s.snapshots[req.SnapshotID]
		if !ok {
			notFound(w, "snapshot")
			return
		}
		if snap.status != "ready" {
			apiErr(w, http.StatusConflict, "operation_conflict", "snapshot is not ready", nil)
			return
		}
		tmpl = snap.template
	} else if tmpl == "" {
		tmpl = "agent-browser"
	} else if !templates[strings.SplitN(tmpl, "@", 2)[0]] {
		apiErr(w, http.StatusBadRequest, "bad_request", "unknown template "+tmpl, nil)
		return
	}
	if req.Name != "" && s.lookup(req.Name) != nil {
		apiErr(w, http.StatusConflict, "operation_conflict", "a sandbox named "+req.Name+" exists", nil)
		return
	}
	if s.opts.MaxMachines > 0 && len(s.machines) >= s.opts.MaxMachines {
		apiErr(w, http.StatusTooManyRequests, "quota_exceeded", "sandbox limit reached ("+strconv.Itoa(s.opts.MaxMachines)+")", nil)
		return
	}
	fail := s.failCreate
	s.failCreate = nil
	if fail != nil && req.FailFast {
		apiErr(w, http.StatusServiceUnavailable, fail.code, "no host has room", map[string]any{"retryable": fail.retryable, "retryAfterSeconds": fail.retryAfter})
		return
	}
	m := s.newMachine(req.Name, strings.SplitN(tmpl, "@", 2)[0], req.SnapshotID)
	if req.VCPUs != 0 {
		m.vcpus, m.memMiB = req.VCPUs, req.MemMiB
	}
	if req.AutoSuspendAfterSeconds != nil {
		v := *req.AutoSuspendAfterSeconds
		m.autoSuspend = &v
	}
	if req.MaxRunMinutes != nil && !s.opts.FreePlan {
		m.maxRun = *req.MaxRunMinutes
	}
	for k, v := range req.Metadata {
		m.metadata[k] = v
	}
	if s.opts.FreePlan && req.SnapshotID == "" && !map[string]bool{"agent-browser": true, "android": true, "omarchy": true, "ci": true}[m.template] {
		apiErr(w, http.StatusForbidden, "template_not_allowed", "the free plan creates from agent-browser, android, omarchy or ci", nil)
		return
	}
	final := "running"
	if req.DesiredState == "stopped" {
		final = "stopped"
	}
	m.desiredState = final
	s.machines[m.id] = m
	op := s.startOp(m, "create", "creating", final)
	if fail != nil {
		op.errBody = map[string]any{"code": fail.code, "message": "injected " + fail.code, "requestId": "req_" + randID(12),
			"retryable": fail.retryable, "operationId": nil, "retryAfterSeconds": fail.retryAfter}
	}
	writeJSON(w, http.StatusAccepted, op.json(m))
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	filters := r.URL.Query()["metadata"]
	var list []*machine
	for _, m := range s.machines {
		match := true
		for _, f := range filters {
			k, v, _ := strings.Cut(f, ":")
			if m.metadata[k] != v {
				match = false
			}
		}
		if match {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	start := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		for start < len(list) && list[start].id <= c {
			start++
		}
	}
	limit := len(list)
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	end := min(start+limit, len(list))
	page := []map[string]any{}
	for _, m := range list[start:end] {
		page = append(page, m.json())
	}
	var next any
	if end < len(list) {
		next = list[end-1].id
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": page, "nextCursor": next})
}

func (s *Server) getMachine(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	m := s.lookup(r.PathValue("id"))
	if m == nil {
		notFound(w, "sandbox")
		return
	}
	writeJSON(w, http.StatusOK, m.json())
}
