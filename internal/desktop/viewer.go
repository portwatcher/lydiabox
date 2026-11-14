package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/desktop/ice"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// refreshRetry is the delay before another try when a credential refresh
// fails.
const refreshRetry = 30 * time.Second

// viewer is the guest signaling session of one browser socket. Before
// minted TURN credentials expire, it opens a new session (new ICE grant,
// new guest peer, new hello); the browser builds a new peer connection on
// each hello.
type viewer struct {
	rl       *Relay
	tid      string
	c        *client
	publicIP string
	cancel   context.CancelFunc

	mu      sync.Mutex
	sig     SignalConn
	grant   ice.Grant
	granted time.Time
	gen     int
	closed  bool
}

// grantICE returns the ICE configuration of a new session and whether the
// guest gets it too.
func (v *viewer) grantICE(ctx context.Context) (ice.Grant, bool, error) {
	if v.rl.opts.ICE == nil {
		return ice.Grant{Servers: v.rl.opts.ICEServers}, false, nil
	}
	g, err := v.rl.opts.ICE.Grant(ctx, v.tid)
	if err == nil {
		return g, true, nil
	}
	if v.rl.opts.RelayOnly {
		return ice.Grant{}, false, err
	}
	v.rl.opts.Logger.Warn("ICE servers unavailable; using the fallback STUN servers", "err", err)
	return ice.Grant{Servers: v.rl.opts.ICEServers}, false, nil
}

// connect opens a guest session and makes it the current one.
func (v *viewer) connect(ctx context.Context) error {
	grant, guestICE, err := v.grantICE(ctx)
	if err != nil {
		return err
	}
	policy := rpc.ICEPolicyAll
	if v.rl.opts.RelayOnly {
		policy = rpc.ICEPolicyRelay
	}
	params := rpc.SignalParams{PublicIP: v.publicIP}
	if guestICE {
		params.ICEServers = grant.Servers
		params.ICETransportPolicy = policy
	}
	if v.rl.opts.RelayOnly {
		params.PublicIP = ""
	}
	sig, hello, err := v.rl.opts.Guest.OpenSignal(ctx, v.tid, params)
	if err != nil {
		grant.Release()
		return err
	}
	servers := grant.Servers
	if servers == nil {
		servers = []ICEServer{}
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		sig.Close()
		grant.Release()
		return errors.New("viewer closed")
	}
	old, oldGrant := v.sig, v.grant
	v.sig, v.grant, v.granted = sig, grant, time.Now()
	v.gen++
	gen := v.gen
	// Queued under the lock: no message of the old session follows it.
	v.c.sendJSON(helloMsg{Type: "hello", Mode: hello.Mode, Screen: hello.Screen, ICEServers: servers, ICETransportPolicy: policy})
	v.mu.Unlock()
	go v.pump(ctx, sig, gen)
	if old != nil {
		old.Close()
		oldGrant.Release()
	}
	return nil
}

// pump relays the messages of one guest session while it is current.
func (v *viewer) pump(ctx context.Context, sig SignalConn, gen int) {
	for {
		msg, err := sig.Recv()
		v.mu.Lock()
		if v.gen != gen || v.closed {
			v.mu.Unlock()
			return
		}
		if err != nil {
			v.mu.Unlock()
			if ctx.Err() == nil {
				v.c.sendJSON(errorMsg{Type: "error", Code: "unavailable", Message: "desktop stream ended"})
			}
			v.cancel()
			return
		}
		v.c.send(msg)
		v.mu.Unlock()
	}
}

// Send forwards a browser message to the current guest session.
func (v *viewer) Send(msg json.RawMessage) error {
	v.mu.Lock()
	sig := v.sig
	v.mu.Unlock()
	return sig.Send(msg)
}

// refreshLoop replaces the session before its credentials expire.
func (v *viewer) refreshLoop(ctx context.Context) {
	for {
		v.mu.Lock()
		exp, granted := v.grant.Expires, v.granted
		v.mu.Unlock()
		if exp.IsZero() {
			return
		}
		if !v.wait(ctx, time.Until(exp)-refreshLead(exp.Sub(granted))) {
			return
		}
		if err := v.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			v.rl.opts.Logger.Warn("refresh desktop ICE credentials", "err", err)
			if !v.wait(ctx, refreshRetry) {
				return
			}
		}
	}
}

func (v *viewer) wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(max(d, 0))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// refreshLead is how long before expiry a session is replaced: a fifth of
// the credential lifetime, at most five minutes.
func refreshLead(ttl time.Duration) time.Duration {
	return min(ttl/5, 5*time.Minute)
}

// close ends the current session and revokes its credentials.
func (v *viewer) close() {
	v.mu.Lock()
	v.closed = true
	sig, grant := v.sig, v.grant
	v.mu.Unlock()
	if sig != nil {
		sig.Close()
	}
	grant.Release()
}
