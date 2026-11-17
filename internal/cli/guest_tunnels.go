package cli

import (
	"context"
	"log/slog"
	"net"
	"sync"
)

// guestTunnels runs the host servers that the guest reaches on host ports
// (the LLM gateway on 9002, the connector broker on 9001). They end when
// the host shuts down; a VM stop or pause does not close their listeners.
type guestTunnels struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	wg     sync.WaitGroup
}

func newGuestTunnels(log *slog.Logger) *guestTunnels {
	ctx, cancel := context.WithCancel(context.Background())
	return &guestTunnels{ctx: ctx, cancel: cancel, log: log}
}

// serve runs serve on ln until stop. A server that ends after stop (for
// example because the driver closed ln during the host shutdown) is
// expected and logged at info level; one that ends before is an error.
func (t *guestTunnels) serve(name string, ln net.Listener, serve func(context.Context, net.Listener) error) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		err := serve(t.ctx, ln)
		switch {
		case err == nil:
		case t.ctx.Err() != nil:
			t.log.Info(name+" stopped with the host", "err", err)
		default:
			t.log.Error(name+" stopped", "err", err)
		}
	}()
}

// stop ends the servers and waits for them (or for ctx), so that the
// driver closes the listeners after the servers have let them go.
func (t *guestTunnels) stop(ctx context.Context) {
	t.cancel()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
