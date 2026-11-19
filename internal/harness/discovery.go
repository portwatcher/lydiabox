package harness

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Discovery finds the on-disk sessions of the harnesses and purges their
// transcripts. LocalDiscovery runs the adapters on the host copy of
// /home/agent (juicefs storage mode); GuestDiscovery asks the guest daemon
// to run them on the machine disk (provider storage mode).
type Discovery interface {
	// Discover returns the sessions of the named harnesses (all when
	// harnesses is empty).
	Discover(ctx context.Context, tenantID string, harnesses []string) (Discovered, error)
	// Purge deletes the native transcript of one session.
	Purge(ctx context.Context, tenantID, harness, nativeID string) error
	// Transcript reads the native transcript of one session.
	Transcript(ctx context.Context, tenantID, harness, nativeID string) (Transcript, error)
}

// Discovered is the result of one discovery.
type Discovered struct {
	// Sessions maps a harness to its sessions.
	Sessions map[string][]SessionMeta
	// Errors maps a harness to the error of its adapter.
	Errors map[string]error
	// Cached reports that the result comes from the last stored discovery
	// because the machine that holds the files is not running.
	Cached bool
	// CachedAt is the time of the oldest cached harness result.
	CachedAt time.Time
}

func wanted(harnesses []string, name string) bool {
	return len(harnesses) == 0 || slices.Contains(harnesses, name)
}

// LocalDiscovery runs the adapters on the host directories of Dirs.
type LocalDiscovery struct {
	Dirs     Dirs
	Adapters []Adapter
	// EventReader reads session events (adapters.EventReader).
	EventReader EventReader
}

// Discover implements Discovery.
func (l LocalDiscovery) Discover(_ context.Context, tenantID string, harnesses []string) (Discovered, error) {
	home, err := l.Dirs.HomeDir(tenantID)
	if err != nil {
		return Discovered{}, err
	}
	out := Discovered{Sessions: map[string][]SessionMeta{}, Errors: map[string]error{}}
	for _, a := range l.Adapters {
		if !wanted(harnesses, a.Name()) {
			continue
		}
		metas, err := a.DiscoverSessions(home)
		if err != nil {
			out.Errors[a.Name()] = err
			continue
		}
		out.Sessions[a.Name()] = metas
	}
	return out, nil
}

// Purge implements Discovery.
func (l LocalDiscovery) Purge(_ context.Context, tenantID, harness, nativeID string) error {
	for _, a := range l.Adapters {
		if a.Name() != harness {
			continue
		}
		home, err := l.Dirs.HomeDir(tenantID)
		if err != nil {
			return err
		}
		return a.Purge(home, nativeID)
	}
	return fmt.Errorf("%w: unknown harness %q", ErrNotFound, harness)
}

// Transcript implements Discovery.
func (l LocalDiscovery) Transcript(_ context.Context, tenantID, harness, nativeID string) (Transcript, error) {
	for _, a := range l.Adapters {
		if a.Name() != harness {
			continue
		}
		tr, ok := a.(Transcriber)
		if !ok {
			return Transcript{}, fmt.Errorf("%w: %s has no transcript reader", ErrNotFound, harness)
		}
		home, err := l.Dirs.HomeDir(tenantID)
		if err != nil {
			return Transcript{}, err
		}
		return tr.Transcript(home, nativeID)
	}
	return Transcript{}, fmt.Errorf("%w: unknown harness %q", ErrNotFound, harness)
}

// Events implements EventSource.
func (l LocalDiscovery) Events(_ context.Context, tenantID, harness, nativeID string, after int, _ bool) (EventPage, error) {
	if l.EventReader == nil {
		return EventPage{}, fmt.Errorf("%w: no event reader", ErrUnavailable)
	}
	if _, ok := l.adapter(harness); !ok {
		return EventPage{}, fmt.Errorf("%w: unknown harness %q", ErrNotFound, harness)
	}
	home, err := l.Dirs.HomeDir(tenantID)
	if err != nil {
		return EventPage{}, err
	}
	evs, err := l.EventReader.Events(home, harness, nativeID)
	if err != nil {
		return EventPage{}, err
	}
	return EventPageOf(evs, after), nil
}

func (l LocalDiscovery) adapter(name string) (Adapter, bool) {
	for _, a := range l.Adapters {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}
