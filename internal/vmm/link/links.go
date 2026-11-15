package link

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/lydiabox/lydiabox/internal/vmm"
)

// Links keeps one Link for each tenant and implements the DialGuest and
// ListenGuestPort methods of vmm.Driver. A remote driver embeds it, calls
// For(tenantID).Connect when the machine is created or resumed, and
// For(tenantID).Suspend before it pauses or stops the machine.
type Links struct {
	newOpts func(tenantID string) Options

	mu    sync.Mutex
	links map[string]*Link
}

// NewLinks returns an empty set. opts returns the options of the link of a
// tenant (the secret of that tenant's guest, timeouts, logger).
func NewLinks(opts func(tenantID string) Options) *Links {
	return &Links{newOpts: opts, links: map[string]*Link{}}
}

// For returns the link of a tenant and makes it on first use.
func (s *Links) For(tenantID string) (*Link, error) {
	if err := vmm.ValidTenantID(tenantID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.links[tenantID]; l != nil {
		return l, nil
	}
	if s.links == nil {
		return nil, net.ErrClosed
	}
	l, err := New(s.newOpts(tenantID))
	if err != nil {
		return nil, err
	}
	s.links[tenantID] = l
	return l, nil
}

// DialGuest implements vmm.Driver.DialGuest.
func (s *Links) DialGuest(ctx context.Context, tenantID string, port uint32) (net.Conn, error) {
	l, err := s.For(tenantID)
	if err != nil {
		return nil, err
	}
	return l.Dial(ctx, port)
}

// ListenGuestPort implements vmm.Driver.ListenGuestPort. The listener
// survives reconnects and can be made before the machine exists.
func (s *Links) ListenGuestPort(_ context.Context, tenantID string, port uint32) (net.Listener, error) {
	l, err := s.For(tenantID)
	if err != nil {
		return nil, err
	}
	return l.Listen(port)
}

// Close closes every link. Later calls fail.
func (s *Links) Close() error {
	s.mu.Lock()
	links := s.links
	s.links = nil
	s.mu.Unlock()
	var errs []error
	for _, l := range links {
		errs = append(errs, l.Close())
	}
	return errors.Join(errs...)
}
