package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/store"
)

// EventCache keeps a host copy of the last known events of each session
// (implemented by *store.Store). It is set only in the provider storage
// mode, where the session files are on the machine disk and the host can
// read them only while the machine runs. A nil EventCache turns the copy
// off. The copy holds conversation content (prompts, replies, tool input
// and output) and no credentials.
type EventCache interface {
	PutSessionEvents(ctx context.Context, tenantID, harness, sessionID string, events []json.RawMessage) error
	GetSessionEvents(ctx context.Context, tenantID, harness, sessionID string) (store.SessionEvents, error)
	DeleteSessionEvents(ctx context.Context, tenantID, harness, sessionID string) error
	ListSessionEventsStamps(ctx context.Context, tenantID string) ([]store.SessionEventsStamp, error)
}

const (
	// defaultEventCopyDelay is the shortest time between two writes of the
	// copy of a watched session, so that a live session does not write the
	// state database at every poll.
	defaultEventCopyDelay = 15 * time.Second
	// eventCopyTimeout bounds one read or write of a copy.
	eventCopyTimeout = 10 * time.Second
)

func (m *Manager) eventCopyDelay() time.Duration {
	if m.opts.EventCopyDelay > 0 {
		return m.opts.EventCopyDelay
	}
	return defaultEventCopyDelay
}

// eventCopies orders the writes and the deletes of the copies. A delete
// increments the generation of the session, and a write that started
// before the delete (a hub or a fill) is then dropped.
type eventCopies struct {
	mu      sync.Mutex
	gens    map[string]uint64 // by liveKey(tenant, source)
	filling map[string]bool   // tenants with a background fill
}

func (c *eventCopies) gen(tenantID, source string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gens[liveKey(tenantID, source)]
}

// preload is the stored copy that a new hub shows before its first read.
// noTranscript reports a provisional session: it has no events to read.
type preload struct {
	source       string
	gen          uint64
	events       []Event
	noTranscript bool
}

// preloadEvents reads the stored copy of a session. A provisional session
// (no native ID yet) and a running ACP session (the bridge sends every
// event at once) have none.
func (m *Manager) preloadEvents(tenantID, id string) preload {
	ctx, cancel := context.WithTimeout(context.Background(), eventCopyTimeout)
	defer cancel()
	name, nativeID, err := SplitSessionID(id)
	if lv, live := m.liveInfo(tenantID, id); live {
		if lv.provisional {
			return preload{noTranscript: true}
		}
		if lv.engine == EngineACP && lv.room != nil {
			return preload{}
		}
		name, nativeID, err = lv.harness, lv.nativeID, nil
	} else if m.storedProvisional(ctx, tenantID, m.aliasTarget(tenantID, id)) {
		return preload{noTranscript: true}
	}
	if m.opts.EventCache == nil || err != nil || nativeID == "" {
		return preload{}
	}
	source := SessionID(name, nativeID)
	p := preload{source: source, gen: m.copies.gen(tenantID, source)}
	got, err := m.opts.EventCache.GetSessionEvents(ctx, tenantID, name, nativeID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("read the stored session events", "session", source, "err", err)
		}
		return p
	}
	for _, raw := range got.Events {
		var e Event
		if json.Unmarshal(raw, &e) == nil && e.Key != "" {
			p.events = append(p.events, e)
		}
	}
	return p
}

// takePreload gives a new hub its stored copy; the caller holds no hub
// lock because nobody else sees the hub yet.
func (h *eventHub) takePreload(p preload) {
	if p.noTranscript {
		// The first update tells the client at once that there is nothing
		// to load.
		h.status.Synced = true
	}
	if p.source == "" {
		return
	}
	h.copyOf, h.gen = p.source, p.gen
	if len(p.events) == 0 {
		return
	}
	h.ev.upsert(p.events)
	h.source, h.cached = p.source, true
	h.status.Cached = true
}

// adoptFresh replaces the preloaded copy with the events of the first
// complete read of the guest. The clients keep the events they have and get
// the changed and new ones when the order of the copy is the start of the
// guest order; otherwise (a key was removed, the order differs, or the
// copy was cut at the start) they get a reset.
func (h *eventHub) adoptFresh(fresh eventList) (reset bool, changed []Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev := h.ev
	h.ev, h.cached = fresh, false
	if len(prev.list) > len(fresh.list) {
		return true, nil
	}
	for i, e := range prev.list {
		if fresh.list[i].Key != e.Key {
			return true, nil
		}
	}
	for _, e := range fresh.list {
		if prev.sums[e.Key] != fresh.sums[e.Key] {
			changed = append(changed, e)
		}
	}
	return false, changed
}

// markDirty records that the events of source changed and need a write.
func (h *eventHub) markDirty(source string) {
	if h.m.opts.EventCache == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.copyOf != source {
		h.copyOf, h.gen = source, h.m.copies.gen(h.tenantID, source)
	}
	h.dirty = true
}

// saver writes the copy of the hub at most once per eventCopyDelay, and
// once more when the hub ends.
func (h *eventHub) saver(ctx context.Context) {
	t := time.NewTicker(h.m.eventCopyDelay())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// The hub's cancel ended ctx: the last write uses its own.
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), eventCopyTimeout)
			h.save(sctx)
			cancel()
			return
		case <-t.C:
			h.save(ctx)
		}
	}
}

// save writes the events of the hub when they changed after the last write.
// A copy that the guest did not confirm yet is not written again.
func (h *eventHub) save(ctx context.Context) {
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	h.mu.Lock()
	if !h.dirty || h.cached || h.copyOf == "" {
		h.mu.Unlock()
		return
	}
	list, source, gen := slices.Clone(h.ev.list), h.copyOf, h.gen
	h.dirty = false
	h.mu.Unlock()
	if err := h.m.putEventCopy(ctx, h.tenantID, source, gen, list); err != nil {
		h.m.log.Warn("store the session events", "session", source, "err", err)
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
	}
}

// putEventCopy writes the copy of source unless a delete came after gen.
func (m *Manager) putEventCopy(ctx context.Context, tenantID, source string, gen uint64, list []Event) error {
	name, nativeID, err := SplitSessionID(source)
	if err != nil {
		return err
	}
	raws := make([]json.RawMessage, 0, len(list))
	for _, e := range list {
		if b, err := json.Marshal(e); err == nil {
			raws = append(raws, b)
		}
	}
	m.copies.mu.Lock()
	defer m.copies.mu.Unlock()
	if m.copies.gens[liveKey(tenantID, source)] != gen {
		return nil
	}
	return m.opts.EventCache.PutSessionEvents(ctx, tenantID, name, nativeID, raws)
}

// dropEventCopy removes the copy of source and stops the writes that
// started before.
func (m *Manager) dropEventCopy(tenantID, source string) {
	name, nativeID, err := SplitSessionID(source)
	if err != nil || m.opts.EventCache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), eventCopyTimeout)
	defer cancel()
	m.copies.mu.Lock()
	defer m.copies.mu.Unlock()
	if m.copies.gens == nil {
		m.copies.gens = map[string]uint64{}
	}
	m.copies.gens[liveKey(tenantID, source)]++
	if err := m.opts.EventCache.DeleteSessionEvents(ctx, tenantID, name, nativeID); err != nil {
		m.log.Warn("delete the stored session events", "session", source, "err", err)
	}
}

// saveHubs writes the changed copies of the watched sessions of a tenant.
func (m *Manager) saveHubs(ctx context.Context, tenantID string) {
	m.hubsMu.Lock()
	var hubs []*eventHub
	for _, h := range m.hubs {
		if h.tenantID == tenantID {
			hubs = append(hubs, h)
		}
	}
	m.hubsMu.Unlock()
	for _, h := range hubs {
		h.save(ctx)
	}
}

// watched reports whether a hub follows the session source; it keeps the
// copy current itself.
func (m *Manager) watched(tenantID, source string) bool {
	m.hubsMu.Lock()
	defer m.hubsMu.Unlock()
	return m.hubs[liveKey(tenantID, source)] != nil
}

var _ EventCache = (*store.Store)(nil)
