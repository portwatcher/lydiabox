package instaboxtest

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GuestRunner runs the programs of fake machines, for example the real
// guest daemon in the test process. The fake calls it outside its locks.
type GuestRunner interface {
	// Boot starts the programs of machine id after a create or a start
	// (not after a resume: a pause keeps the memory and the processes).
	// files reads a file that the file API wrote to the machine.
	Boot(id string, files func(path string) ([]byte, bool))
	// Halt stops the programs of machine id (stop, delete, or a failed
	// operation).
	Halt(id string)
	// Addr returns the local address that serves guest port port of
	// machine id, or "" when nothing listens there yet.
	Addr(id string, port int) string
}

// portProxy is the fake of the InstaBox port proxy: it serves each
// published port at <ProxyURL>/p/<port>-<machine id without m_>/.
type portProxy struct {
	srv *httptest.Server

	mu    sync.Mutex
	conns map[string]map[net.Conn]bool // machine ID -> backend connections
}

type guestEvent struct {
	boot bool
	id   string
}

// startGuests starts the port proxy and the goroutine that runs the
// GuestRunner events.
func (s *Server) startGuests() {
	s.proxy = &portProxy{conns: map[string]map[net.Conn]bool{}}
	s.proxy.srv = httptest.NewServer(http.HandlerFunc(s.serveProxy))
	s.guestWake = make(chan struct{}, 1)
	s.guestDone = make(chan struct{})
	go s.runGuestEvents()
}

// ProxyURL returns the base URL of the port proxy ("" without a
// GuestRunner).
func (s *Server) ProxyURL() string {
	if s.proxy == nil {
		return ""
	}
	return s.proxy.srv.URL
}

// Close stops the fake, its port proxy, and every machine program.
func (s *Server) Close() {
	s.Server.Close()
	if s.proxy == nil {
		return
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.machines))
	for id := range s.machines {
		ids = append(ids, id)
	}
	s.closed = true
	s.mu.Unlock()
	close(s.guestWake)
	<-s.guestDone
	for _, id := range ids {
		s.proxy.closeConns(id)
		s.opts.Guest.Halt(id)
	}
	s.proxy.srv.CloseClientConnections()
	s.proxy.srv.Close()
}

// queueGuest records a boot or a halt. Callers hold s.mu.
func (s *Server) queueGuest(boot bool, id string) {
	if s.proxy == nil || s.closed {
		return
	}
	if !boot {
		s.proxy.closeConns(id)
	}
	s.guestQueue = append(s.guestQueue, guestEvent{boot: boot, id: id})
	select {
	case s.guestWake <- struct{}{}:
	default:
	}
}

func (s *Server) runGuestEvents() {
	defer close(s.guestDone)
	for range s.guestWake {
		for {
			s.mu.Lock()
			if len(s.guestQueue) == 0 {
				s.mu.Unlock()
				break
			}
			ev := s.guestQueue[0]
			s.guestQueue = s.guestQueue[1:]
			s.mu.Unlock()
			if ev.boot {
				id := ev.id
				s.opts.Guest.Boot(id, func(p string) ([]byte, bool) {
					b, _, ok := s.File(id, p)
					return b, ok
				})
			} else {
				s.opts.Guest.Halt(ev.id)
			}
		}
	}
}

// pauseConns closes the connections through the port proxy of a machine
// that pauses: a paused VM answers nothing.
func (s *Server) pauseConns(id string) {
	if s.proxy != nil {
		s.proxy.closeConns(id)
	}
}

func (p *portProxy) track(id string, c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conns[id] == nil {
		p.conns[id] = map[net.Conn]bool{}
	}
	p.conns[id][c] = true
}

func (p *portProxy) closeConns(id string) {
	p.mu.Lock()
	conns := p.conns[id]
	delete(p.conns, id)
	p.mu.Unlock()
	for c := range conns {
		c.Close()
	}
}

// portURL is the URL of a published port.
func (s *Server) portURL(m *machine, port int) string {
	suffix := strconv.Itoa(port) + "-" + strings.TrimPrefix(m.id, "m_")
	if s.proxy != nil {
		return s.proxy.srv.URL + "/p/" + suffix
	}
	return "https://" + suffix + ".fake.instabox.test"
}

// serveProxy answers a request to a published port. A private port needs
// its token (Authorization: Bearer, or the _token query once); a request
// without it gets 401 and does not wake the machine. A request with it
// wakes a machine that paused itself; a machine that a client paused or
// stopped answers 503 machine_not_running.
func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/p/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	seg, tail, _ := strings.Cut(rest, "/")
	portStr, suffix, _ := strings.Cut(seg, "-")
	num, err := strconv.Atoi(portStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id := "m_" + suffix
	s.mu.Lock()
	s.advance()
	m := s.machines[id]
	var pp *port
	if m != nil {
		pp = m.ports[num]
	}
	if pp == nil {
		s.mu.Unlock()
		apiErr(w, http.StatusNotFound, "not_found", "port not published", nil)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if q := r.URL.Query().Get("_token"); q != "" {
		token = q
	}
	if pp.access == "private" && token != pp.token {
		s.mu.Unlock()
		http.Error(w, "missing or invalid port token", http.StatusUnauthorized)
		return
	}
	if m.state == "suspended" && m.autoSuspendedAt != nil {
		s.wakeLocked(m, "request")
	}
	state := m.state
	s.mu.Unlock()
	if state != "running" {
		apiErr(w, http.StatusServiceUnavailable, "machine_not_running", "sandbox is "+state, nil)
		return
	}
	addr := s.opts.Guest.Addr(id, num)
	if addr == "" {
		http.Error(w, "nothing listens on port "+portStr, http.StatusBadGateway)
		return
	}
	r.Header.Del("Authorization")
	target := &url.URL{Scheme: "http", Host: addr}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + tail
			pr.Out.URL.RawPath = ""
		},
		Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
			c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, a)
			if err == nil {
				s.proxy.track(id, c)
			}
			return c, err
		}},
	}
	rp.ServeHTTP(w, r)
}

// wakeLocked resumes a machine that paused itself. Callers hold s.mu.
func (s *Server) wakeLocked(m *machine, trigger string) {
	now := time.Now().UTC()
	o := &operation{id: "op_" + randID(26), machineID: m.id, typ: "resume", state: "succeeded", trigger: trigger,
		final: "running", doneAt: now, createdAt: now, updatedAt: now}
	s.ops[o.id] = o
	m.state, m.autoSuspendedAt, m.runStartedAt = "running", nil, &now
	m.revision++
	s.wakes++
}

// Wakes counts the resumes of self-paused machines on use.
func (s *Server) Wakes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wakes
}

// AddSnapshot adds a ready snapshot (as an image import makes one) and
// returns its ID.
func (s *Server) AddSnapshot(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	sn := &snapshot{id: "sn_" + randID(26), name: name, orgID: "org_fake", template: "agent-browser",
		status: "ready", createdAt: now, readyAt: now}
	s.snapshots[sn.id] = sn
	return sn.id
}

// Machines returns the IDs of the live machines.
func (s *Server) Machines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	out := make([]string, 0, len(s.machines))
	for id := range s.machines {
		out = append(out, id)
	}
	return out
}
