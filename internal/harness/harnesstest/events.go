package harnesstest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/store"
)

// EventCache is an in-memory harness.EventCache.
type EventCache struct {
	mu     sync.Mutex
	copies map[string]store.SessionEvents
	puts   int
}

// PutSessionEvents implements harness.EventCache.
func (c *EventCache) PutSessionEvents(_ context.Context, tenantID, h, id string, events []json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.copies == nil {
		c.copies = map[string]store.SessionEvents{}
	}
	c.copies[key(tenantID, h, id)] = store.SessionEvents{Events: slices.Clone(events), UpdatedAt: time.Now().UTC()}
	c.puts++
	return nil
}

// GetSessionEvents implements harness.EventCache.
func (c *EventCache) GetSessionEvents(_ context.Context, tenantID, h, id string) (store.SessionEvents, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	got, ok := c.copies[key(tenantID, h, id)]
	if !ok {
		return store.SessionEvents{}, store.ErrNotFound
	}
	return got, nil
}

// DeleteSessionEvents implements harness.EventCache.
func (c *EventCache) DeleteSessionEvents(_ context.Context, tenantID, h, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.copies, key(tenantID, h, id))
	return nil
}

// ListSessionEventsStamps implements harness.EventCache.
func (c *EventCache) ListSessionEventsStamps(_ context.Context, tenantID string) ([]store.SessionEventsStamp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []store.SessionEventsStamp
	for k, v := range c.copies {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) == 3 && parts[0] == tenantID {
			out = append(out, store.SessionEventsStamp{Harness: parts[1], SessionID: parts[2], UpdatedAt: v.UpdatedAt})
		}
	}
	return out, nil
}

// Set stores a copy with the time at.
func (c *EventCache) Set(tenantID, h, id string, at time.Time, events ...harness.Event) {
	raws := make([]json.RawMessage, 0, len(events))
	for _, e := range events {
		b, _ := json.Marshal(e)
		raws = append(raws, b)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.copies == nil {
		c.copies = map[string]store.SessionEvents{}
	}
	c.copies[key(tenantID, h, id)] = store.SessionEvents{Events: raws, UpdatedAt: at}
}

// Keys returns the event keys of a stored copy, and whether it exists.
func (c *EventCache) Keys(tenantID, h, id string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	got, ok := c.copies[key(tenantID, h, id)]
	var out []string
	for _, raw := range got.Events {
		var e harness.Event
		_ = json.Unmarshal(raw, &e)
		out = append(out, e.Key)
	}
	return out, ok
}

// Puts returns the number of writes.
func (c *EventCache) Puts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts
}

// EventDiscovery is a fake guest discovery (provider storage mode) with
// session events. A read with wake waits until Resume while the machine
// does not run; a read without wake fails then.
type EventDiscovery struct {
	mu       sync.Mutex
	running  bool
	resumed  chan struct{}
	sessions map[string][]harness.SessionMeta
	events   map[string][]harness.Event
	wakes    int
	reads    map[string]int
	// WakeErr, when set, fails a read with wake while the machine does not
	// run (the user stopped it).
	WakeErr error
	// OnRead runs after every read of a session (outside the lock).
	OnRead func(source string)
}

// NewEventDiscovery returns a fake whose machine runs or not.
func NewEventDiscovery(running bool) *EventDiscovery {
	d := &EventDiscovery{running: running, resumed: make(chan struct{}), sessions: map[string][]harness.SessionMeta{},
		events: map[string][]harness.Event{}, reads: map[string]int{}}
	if running {
		close(d.resumed)
	}
	return d
}

// SetSession sets the events of a session and its time in the discovery.
func (d *EventDiscovery) SetSession(h, id string, updated time.Time, events ...harness.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	metas := d.sessions[h][:0:0]
	for _, sm := range d.sessions[h] {
		if sm.NativeID != id {
			metas = append(metas, sm)
		}
	}
	d.sessions[h] = append(metas, harness.SessionMeta{Harness: h, NativeID: id, CreatedAt: updated, UpdatedAt: updated})
	d.events[harness.SessionID(h, id)] = slices.Clone(events)
}

// Resume lets the machine run (a waiting wake returns).
func (d *EventDiscovery) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		d.running = true
		close(d.resumed)
	}
}

// Pause stops the machine.
func (d *EventDiscovery) Pause() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		d.running = false
		d.resumed = make(chan struct{})
	}
}

// Wakes returns the number of reads that asked to wake the machine.
func (d *EventDiscovery) Wakes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wakes
}

// Reads returns the number of event reads of a session.
func (d *EventDiscovery) Reads(source string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads[source]
}

// Discover implements harness.Discovery: fresh while the machine runs,
// otherwise cached.
func (d *EventDiscovery) Discover(_ context.Context, _ string, harnesses []string) (harness.Discovered, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := harness.Discovered{Sessions: map[string][]harness.SessionMeta{}, Errors: map[string]error{}, Cached: !d.running}
	for h, metas := range d.sessions {
		if len(harnesses) == 0 || slices.Contains(harnesses, h) {
			out.Sessions[h] = slices.Clone(metas)
		}
	}
	return out, nil
}

// Purge implements harness.Discovery.
func (d *EventDiscovery) Purge(_ context.Context, _, h, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.events, harness.SessionID(h, id))
	return nil
}

// Transcript implements harness.Discovery.
func (d *EventDiscovery) Transcript(context.Context, string, string, string) (harness.Transcript, error) {
	return harness.Transcript{}, nil
}

// Events implements harness.EventSource.
func (d *EventDiscovery) Events(ctx context.Context, _, h, id string, after int, wake bool) (harness.EventPage, error) {
	source := harness.SessionID(h, id)
	d.mu.Lock()
	if wake {
		d.wakes++
	}
	running, resumed, wakeErr := d.running, d.resumed, d.WakeErr
	d.mu.Unlock()
	if !running {
		if wake && wakeErr != nil {
			return harness.EventPage{}, fmt.Errorf("%w: %v", harness.ErrUnavailable, wakeErr)
		}
		if !wake {
			return harness.EventPage{}, fmt.Errorf("%w: the machine does not run", harness.ErrUnavailable)
		}
		select {
		case <-ctx.Done():
			return harness.EventPage{}, ctx.Err()
		case <-resumed:
		}
	}
	d.mu.Lock()
	d.reads[source]++
	evs, ok := d.events[source]
	evs = slices.Clone(evs)
	onRead := d.OnRead
	d.mu.Unlock()
	if onRead != nil {
		onRead(source)
	}
	if !ok {
		return harness.EventPage{}, fmt.Errorf("%w: session %s", harness.ErrNotFound, source)
	}
	return harness.EventPageOf(evs, after), nil
}
