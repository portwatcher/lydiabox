package desktop

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/tenant"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
)

// hub is the set of browsers of one tenant plus the guest event
// subscription that feeds them.
type hub struct {
	clients map[*client]struct{}
	cancel  context.CancelFunc
}

func (rl *Relay) join(tid string, c *client) {
	rl.mu.Lock()
	h := rl.hubs[tid]
	if h == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h = &hub{clients: map[*client]struct{}{}, cancel: cancel}
		rl.hubs[tid] = h
		go rl.pumpEvents(ctx, tid)
	}
	h.clients[c] = struct{}{}
	n := len(h.clients)
	rl.mu.Unlock()
	rl.broadcast(tid, viewersMsg{Type: "viewers", Count: n})
}

func (rl *Relay) leave(tid string, c *client) {
	rl.mu.Lock()
	h := rl.hubs[tid]
	if h == nil {
		rl.mu.Unlock()
		return
	}
	delete(h.clients, c)
	n := len(h.clients)
	if n == 0 {
		h.cancel()
		delete(rl.hubs, tid)
	}
	rl.mu.Unlock()
	if n > 0 {
		rl.broadcast(tid, viewersMsg{Type: "viewers", Count: n})
	}
}

func (rl *Relay) broadcast(tid string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	rl.broadcastRaw(tid, b)
}

func (rl *Relay) broadcastRaw(tid string, b []byte) {
	rl.mu.Lock()
	var cs []*client
	if h := rl.hubs[tid]; h != nil {
		for c := range h.clients {
			cs = append(cs, c)
		}
	}
	rl.mu.Unlock()
	for _, c := range cs {
		c.send(b)
	}
}

// pumpEvents relays guest agent_action and mode events to the tenant's
// browsers, resubscribing while any browser is connected.
func (rl *Relay) pumpEvents(ctx context.Context, tid string) {
	for ctx.Err() == nil {
		ev, err := rl.opts.Guest.SubscribeEvents(ctx, tid)
		if err == nil {
			stop := context.AfterFunc(ctx, func() { ev.Close() })
			for {
				msg, err := ev.Recv()
				if err != nil {
					break
				}
				rl.broadcastRaw(tid, msg)
			}
			stop()
			ev.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// ServeMode handles GET and PUT /api/v1/desktop/mode.
func (rl *Relay) ServeMode(w http.ResponseWriter, r *http.Request) {
	tid := tenant.FromContext(r.Context())
	if !rl.opts.Running(tid) {
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, "VM is not running")
		return
	}
	var (
		st  rpc.ModeState
		err error
	)
	if r.Method == http.MethodPut {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
			return
		}
		st, err = rl.opts.Guest.SetMode(r.Context(), tid, req.Mode, "user")
	} else {
		st, err = rl.opts.Guest.GetMode(r.Context(), tid)
	}
	if err != nil {
		if errCode(err) == "bad_request" {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, errMessage(err))
			return
		}
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, errMessage(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// GuestAdapter adapts *guestclient.Client to Guest.
type GuestAdapter struct{ Client *guestclient.Client }

// OpenSignal implements Guest.
func (g GuestAdapter) OpenSignal(ctx context.Context, tid, publicIP string) (SignalConn, rpc.SignalHello, error) {
	s, err := g.Client.OpenSignal(ctx, tid, publicIP)
	if err != nil {
		return nil, rpc.SignalHello{}, err
	}
	return s, s.Hello, nil
}

// SubscribeEvents implements Guest.
func (g GuestAdapter) SubscribeEvents(ctx context.Context, tid string) (EventConn, error) {
	ev, err := g.Client.SubscribeEvents(ctx, tid)
	if err != nil {
		return nil, err
	}
	return ev, nil
}

// GetMode implements Guest.
func (g GuestAdapter) GetMode(ctx context.Context, tid string) (rpc.ModeState, error) {
	return g.Client.GetMode(ctx, tid)
}

// SetMode implements Guest.
func (g GuestAdapter) SetMode(ctx context.Context, tid, mode, by string) (rpc.ModeState, error) {
	return g.Client.SetMode(ctx, tid, mode, by)
}

// LaunchApp implements Guest.
func (g GuestAdapter) LaunchApp(ctx context.Context, tid, app string) error {
	return g.Client.LaunchApp(ctx, tid, app)
}

// RequestIP returns the IP address of the host the browser connected to
// (the request Host), resolving a host name. keepLoopback controls whether
// a loopback address is returned or replaced by "" (a chv guest is reached
// directly over its TAP address from the host itself).
func RequestIP(r *http.Request, keepLoopback bool) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return ""
		}
		for _, a := range addrs {
			if a.IP.To4() != nil {
				ip = a.IP
				break
			}
		}
	}
	if ip == nil || ip.To4() == nil {
		return ""
	}
	if ip.IsLoopback() && !keepLoopback {
		return ""
	}
	return ip.String()
}
