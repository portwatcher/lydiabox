package instaboxtest

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type port struct {
	id, name, access, token string
	port                    int
	createdAt               time.Time
}

func (p *port) json(url string, m *machine, withToken bool) map[string]any {
	out := map[string]any{"id": p.id, "machineId": m.id, "port": p.port, "name": p.name, "url": url,
		"access": p.access, "protocol": "http", "createdAt": p.createdAt, "expiresAt": nil}
	if withToken && p.token != "" {
		out["token"] = p.token
		out["tokenUrl"] = url + "/?_token=" + p.token
	}
	return out
}

type publishReq struct {
	Port        int    `json:"port"`
	Name        string `json:"name"`
	Access      string `json:"access"`
	Protocol    string `json:"protocol"`
	RotateToken bool   `json:"rotateToken"`
}

func (s *Server) publishPort(w http.ResponseWriter, r *http.Request) {
	var req publishReq
	if !decode(w, r, &req, false) {
		return
	}
	if req.Port < 1024 || req.Port > 65535 || req.Port == 5901 || req.Port == 6768 {
		apiErr(w, http.StatusBadRequest, "bad_request", "port "+strconv.Itoa(req.Port)+" cannot be published", nil)
		return
	}
	if req.Access != "" && req.Access != "public" && req.Access != "private" {
		apiErr(w, http.StatusBadRequest, "bad_request", "access must be public or private", nil)
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
	p, existed := m.ports[req.Port]
	if !existed {
		if len(m.ports) >= 16 {
			apiErr(w, http.StatusTooManyRequests, "quota_exceeded", "at most 16 published ports", nil)
			return
		}
		p = &port{id: "pp_" + randID(20), port: req.Port, name: "Port " + strconv.Itoa(req.Port), access: "public", createdAt: time.Now().UTC()}
		m.ports[req.Port] = p
	}
	if req.Name != "" {
		p.name = req.Name
	}
	newToken := false
	if req.Access != "" && req.Access != p.access {
		p.access = req.Access
		p.id = "pp_" + randID(20)
		p.token = ""
		if p.access == "private" {
			p.token, newToken = "ibp_"+randHex(24), true
		}
	} else if !existed && p.access == "private" {
		p.token, newToken = "ibp_"+randHex(24), true
	}
	if req.RotateToken {
		if p.access != "private" {
			apiErr(w, http.StatusBadRequest, "bad_request", "rotateToken needs a private port", nil)
			return
		}
		p.token, newToken = "ibp_"+randHex(24), true
		p.id = "pp_" + randID(20)
	}
	status := http.StatusOK
	if !existed {
		status = http.StatusCreated
	}
	writeJSON(w, status, p.json(s.portURL(m, p.port), m, newToken))
}

func (s *Server) listPorts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	m := s.lookup(r.PathValue("id"))
	if m == nil {
		notFound(w, "sandbox")
		return
	}
	nums := make([]int, 0, len(m.ports))
	for n := range m.ports {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	ports := []map[string]any{}
	for _, n := range nums {
		ports = append(ports, m.ports[n].json(s.portURL(m, n), m, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "ports": ports})
}

func (s *Server) unpublishPort(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	m := s.lookup(r.PathValue("id"))
	n, _ := strconv.Atoi(r.PathValue("port"))
	if m == nil || m.ports[n] == nil {
		notFound(w, "port")
		return
	}
	delete(m.ports, n)
	w.WriteHeader(http.StatusNoContent)
}

// PortToken returns the token of a private port (the server of the real
// API keeps only its hash).
func (s *Server) PortToken(id string, n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.lookup(id); m != nil && m.ports[n] != nil {
		return m.ports[n].token
	}
	return ""
}

type snapshot struct {
	id, name, description, orgID, sourceMachineID, template, status string
	rootOnly                                                        bool
	readyAt, createdAt                                              time.Time
}

func (sn *snapshot) json() map[string]any {
	out := map[string]any{"id": sn.id, "name": sn.name, "orgId": sn.orgID, "sourceMachineId": sn.sourceMachineID,
		"sourceSnapshotSeq": 1, "rootSizeBytes": int64(16) << 30, "createdAt": sn.createdAt, "status": sn.status,
		"template":   map[string]any{"id": sn.template, "version": "2026-11-01.1", "digest": "sha256:" + strings.Repeat("0", 64)},
		"visibility": "private", "guestUser": "user"}
	if sn.description != "" {
		out["description"] = sn.description
	}
	if !sn.rootOnly {
		out["dataSizeBytes"] = int64(20) << 30
	}
	return out
}

// advanceSnapshots makes pending snapshots ready. Callers hold s.mu.
func (s *Server) advanceSnapshots() {
	now := time.Now()
	for _, sn := range s.snapshots {
		if sn.status == "pending" && !now.Before(sn.readyAt) {
			sn.status = "ready"
		}
	}
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		RootOnly    bool   `json:"rootOnly"`
		ForFork     bool   `json:"forFork"`
	}
	if !decode(w, r, &req, false) {
		return
	}
	if !nameRE.MatchString(req.Name) {
		apiErr(w, http.StatusBadRequest, "bad_request", "invalid snapshot name", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	s.advanceSnapshots()
	m := s.lookup(r.PathValue("id"))
	if m == nil {
		notFound(w, "sandbox")
		return
	}
	for _, sn := range s.snapshots {
		if sn.name == req.Name {
			apiErr(w, http.StatusConflict, "operation_conflict", "a snapshot named "+req.Name+" exists", nil)
			return
		}
	}
	if s.opts.MaxSnapshots > 0 && len(s.snapshots) >= s.opts.MaxSnapshots {
		apiErr(w, http.StatusTooManyRequests, "quota_exceeded", "snapshot limit reached", nil)
		return
	}
	if m.opID != "" || (m.state != "running" && m.state != "stopped") {
		apiErr(w, http.StatusConflict, "operation_conflict", "sandbox is "+m.state, nil)
		return
	}
	now := time.Now().UTC()
	sn := &snapshot{id: "sn_" + randID(26), name: req.Name, description: req.Description, orgID: "org_fake",
		sourceMachineID: m.id, template: m.template, status: "ready", rootOnly: req.RootOnly, createdAt: now, readyAt: now}
	status := http.StatusCreated
	if m.state == "running" {
		sn.status, sn.readyAt, status = "pending", now.Add(s.opts.OpDelay), http.StatusAccepted
	}
	s.snapshots[sn.id] = sn
	writeJSON(w, status, sn.json())
}

func (s *Server) listSnapshots(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advanceSnapshots()
	list := make([]*snapshot, 0, len(s.snapshots))
	for _, sn := range s.snapshots {
		list = append(list, sn)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].createdAt.Before(list[j].createdAt) })
	out := []map[string]any{}
	for _, sn := range list {
		out = append(out, sn.json())
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advanceSnapshots()
	sn, ok := s.snapshots[r.PathValue("id")]
	if !ok {
		notFound(w, "snapshot")
		return
	}
	writeJSON(w, http.StatusOK, sn.json())
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sn, ok := s.snapshots[r.PathValue("id")]
	if !ok {
		notFound(w, "snapshot")
		return
	}
	confirm := r.Header.Values("X-Confirm-Delete")
	if len(confirm) != 1 || (confirm[0] != sn.id && confirm[0] != sn.name) {
		apiErr(w, http.StatusConflict, "delete_confirmation_required", "send X-Confirm-Delete: "+sn.id, nil)
		return
	}
	delete(s.snapshots, sn.id)
	w.WriteHeader(http.StatusNoContent)
}
