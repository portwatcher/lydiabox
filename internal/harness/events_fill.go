package harness

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Bounds of the fill of the stored copies (EventCache) for sessions that
// nobody watches.
const (
	// eventFillMax is the most sessions that one fill after a discovery
	// reads, and eventFillBudget its total time.
	eventFillMax    = 20
	eventFillBudget = 2 * time.Minute
	// eventFillReadTimeout bounds the read of one session.
	eventFillReadTimeout = 30 * time.Second
	// eventFillMinAge is the youngest copy that a fill after a discovery
	// reads again; a session that runs without a watcher is then read at
	// most once a minute.
	eventFillMinAge = time.Minute
	// prePauseFillMax and prePauseFillBudget bound the fill before the host
	// pauses or stops the machine. The flush that follows it has the rest
	// of vmm.FlushTimeout (30 s).
	prePauseFillMax    = 10
	prePauseFillBudget = 10 * time.Second
)

// fillItem is one session that a fill can read.
type fillItem struct {
	harness, nativeID string
	updated           time.Time
}

// pageReader reads one page of the events of a session without a wake.
type pageReader func(ctx context.Context, harness, nativeID string, after int) (EventPage, error)

// guestPeeker reads the guest without the Running check, for the fill
// before a pause or stop: the controller then reports the machine as not
// serving, but the guest still runs. Neither call wakes the machine or
// takes a lease (implemented by *GuestDiscovery).
type guestPeeker interface {
	peekDiscover(ctx context.Context, tenantID string, harnesses []string) (Discovered, error)
	peekEvents(ctx context.Context, tenantID, harness, nativeID string, after int) (EventPage, error)
}

var _ guestPeeker = (*GuestDiscovery)(nil)

func fillItems(discovered map[string][]SessionMeta) []fillItem {
	var out []fillItem
	for name, metas := range discovered {
		for _, sm := range metas {
			if sm.NativeID != "" {
				out = append(out, fillItem{harness: name, nativeID: sm.NativeID, updated: sm.UpdatedAt})
			}
		}
	}
	return out
}

// fillAfterDiscovery starts a background fill after a fresh discovery (the
// machine runs). It reads the sessions whose copy is older than their
// transcript, newest first, one at a time and without a wake, and stops
// when the machine does not run. One fill per tenant runs at a time.
func (m *Manager) fillAfterDiscovery(tenantID string, discovered map[string][]SessionMeta) {
	src, ok := m.opts.Discovery.(EventSource)
	if m.opts.EventCache == nil || !ok {
		return
	}
	m.copies.mu.Lock()
	if m.copies.filling[tenantID] {
		m.copies.mu.Unlock()
		return
	}
	if m.copies.filling == nil {
		m.copies.filling = map[string]bool{}
	}
	m.copies.filling[tenantID] = true
	m.copies.mu.Unlock()
	items := fillItems(discovered)
	go func() {
		defer func() {
			m.copies.mu.Lock()
			delete(m.copies.filling, tenantID)
			m.copies.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), eventFillBudget)
		defer cancel()
		m.fillEvents(ctx, tenantID, items, eventFillMax, eventFillMinAge, func(ctx context.Context, h, n string, after int) (EventPage, error) {
			return src.Events(ctx, tenantID, h, n, after, false)
		})
	}()
}

// FillEventCacheBeforePause writes the copies of the sessions that changed
// since their last copy, before the host pauses or stops the machine of a
// tenant (vmm.Options.Flush). It is bounded by prePauseFillBudget and
// prePauseFillMax, does nothing without an EventCache, and never wakes the
// machine.
func (m *Manager) FillEventCacheBeforePause(ctx context.Context, tenantID string) {
	p, ok := m.opts.Discovery.(guestPeeker)
	if m.opts.EventCache == nil || !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, prePauseFillBudget)
	defer cancel()
	m.saveHubs(ctx, tenantID)
	var names []string
	for _, name := range Harnesses {
		if _, ok := m.adapters[name]; ok {
			names = append(names, name)
		}
	}
	found, err := p.peekDiscover(ctx, tenantID, names)
	if err != nil {
		m.log.Info("discover the sessions before a pause", "err", err)
		return
	}
	dismissed, err := m.dismissed(ctx, tenantID)
	if err != nil {
		return
	}
	discovered := map[string][]SessionMeta{}
	for _, name := range names {
		discovered[name] = dismissed.filter(name, found.Sessions[name])
	}
	m.fillEvents(ctx, tenantID, fillItems(discovered), prePauseFillMax, 0, func(ctx context.Context, h, n string, after int) (EventPage, error) {
		return p.peekEvents(ctx, tenantID, h, n, after)
	})
}

// fillEvents reads and stores the events of the stale sessions of items: no
// copy, or a copy older than the transcript and at least minAge old. A
// watched session is left to its hub. It returns the number of copies
// written.
func (m *Manager) fillEvents(ctx context.Context, tenantID string, items []fillItem, limit int, minAge time.Duration, read pageReader) int {
	stamps, err := m.opts.EventCache.ListSessionEventsStamps(ctx, tenantID)
	if err != nil {
		m.log.Warn("list the stored session events", "err", err)
		return 0
	}
	have := make(map[string]time.Time, len(stamps))
	for _, s := range stamps {
		have[SessionID(s.Harness, s.SessionID)] = s.UpdatedAt
	}
	now := time.Now()
	var todo []fillItem
	for _, it := range items {
		source := SessionID(it.harness, it.nativeID)
		if at, ok := have[source]; ok && (!it.updated.After(at) || now.Sub(at) < minAge) {
			continue
		}
		if !m.watched(tenantID, source) {
			todo = append(todo, it)
		}
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].updated.After(todo[j].updated) })
	written := 0
	for _, it := range todo[:min(len(todo), limit)] {
		if ctx.Err() != nil {
			break
		}
		source := SessionID(it.harness, it.nativeID)
		gen := m.copies.gen(tenantID, source)
		var mu sync.Mutex
		l := newEventList()
		rctx, cancel := context.WithTimeout(ctx, eventFillReadTimeout)
		_, _, err := readPages(rctx, &mu, &l, 0, func(ctx context.Context, after int) (EventPage, error) {
			return read(ctx, it.harness, it.nativeID, after)
		})
		cancel()
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case errors.Is(err, ErrUnavailable), ctx.Err() != nil:
			// The machine does not run (or the time is over): stop.
			return written
		case err != nil:
			m.log.Info("read the session events for the stored copy", "session", source, "err", err)
			continue
		}
		if m.watched(tenantID, source) {
			continue
		}
		if err := m.putEventCopy(ctx, tenantID, source, gen, l.list); err != nil {
			m.log.Warn("store the session events", "session", source, "err", err)
			continue
		}
		written++
	}
	return written
}
