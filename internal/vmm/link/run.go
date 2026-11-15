package link

import (
	"context"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	guestlink "github.com/lydiabox/lydiabox/internal/guest/link"
)

// run connects, serves, and reconnects until ctx ends.
func (l *Link) run(ctx context.Context, t Target, done chan struct{}) {
	defer close(done)
	backoff := l.opts.MinBackoff
	for ctx.Err() == nil {
		l.mu.Lock()
		l.attempt = time.Now()
		l.mu.Unlock()
		sess, err := l.connect(ctx, t)
		if ctx.Err() != nil {
			if sess != nil {
				sess.Close()
			}
			return
		}
		if err == nil {
			backoff = l.opts.MinBackoff
			l.serve(ctx, sess)
			if ctx.Err() != nil {
				return
			}
			l.opts.Logger.Warn("guest link lost; reconnecting", "url", t.URL)
		} else {
			l.opts.Logger.Warn("guest link attempt failed", "url", t.URL, "err", err)
			l.mu.Lock()
			l.setStateLocked(StateDown, errString(err))
			l.unlock()
		}
		if !l.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, l.opts.MaxBackoff)
	}
}

// sleep waits for d, a kick, or the end of ctx.
func (l *Link) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-l.kick:
	case <-ctx.Done():
		return false
	}
	return true
}

// connect dials the WebSocket, runs the handshake, and starts the session.
func (l *Link) connect(ctx context.Context, t Target) (*yamux.Session, error) {
	dctx, cancel := context.WithTimeout(ctx, l.opts.WakeTimeout)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, t.URL, &websocket.DialOptions{
		HTTPClient: l.opts.HTTPClient, HTTPHeader: t.Header, Subprotocols: []string{guestlink.Subprotocol},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial: HTTP %s: %w", resp.Status, err)
		}
		return nil, fmt.Errorf("dial: %w", err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	hctx, hcancel := context.WithTimeout(ctx, guestlink.HandshakeTimeout)
	err = guestlink.HostHandshake(hctx, c, l.opts.Secret)
	hcancel()
	if err != nil {
		c.Close(guestlink.CloseUnauthorized, "handshake")
		return nil, err
	}
	sess, err := yamux.Client(websocket.NetConn(ctx, c, websocket.MessageBinary), guestlink.YamuxConfig(l.opts.KeepAlive))
	if err != nil {
		c.Close(websocket.StatusInternalError, "session")
		return nil, err
	}
	return sess, nil
}

// serve publishes the session and routes guest streams until the session
// ends. It marks the link down in the same step that withdraws the session.
func (l *Link) serve(ctx context.Context, sess *yamux.Session) {
	l.mu.Lock()
	l.sess = sess
	l.status.Connects++
	l.setStateLocked(StateUp, "")
	l.unlock()
	l.opts.Logger.Info("guest link up")
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	l.ports.Serve(sess)
	stop()
	sess.Close()
	l.mu.Lock()
	l.sess = nil
	l.setStateLocked(StateDown, "guest link closed")
	l.unlock()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
