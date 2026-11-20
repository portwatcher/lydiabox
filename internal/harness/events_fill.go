package harness

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Bounds of the fill of the stored copies (EventCache) for sessions that
// nobody watches. A fill reads the guest only while the machine runs: it
// never wakes the machine, never holds a lease, and never runs in the pause
// or stop path.
const (
	// eventFillMax is the most sessions that one fill reads, and
	// eventFillBudget its total time.
	eventFillMax    = 20
	eventFillBudget = 2 * time.Minute
	// eventFillReadTimeout bounds the read of one session.
	eventFillReadTimeout = 30 * time.Second
	// eventFillMinAge is the youngest copy that a fill after a discovery
	// reads again; a session that runs without a watcher is then read at
	// most once a minute.
	eventFillMinAge = time.Minute
	// defaultEventFillDelay is the wait between the detach or end of a
	// terminal and the fill of its copy: the harness can write the end of
	// its transcript after the terminal ended.
	defaultEventFillDelay = 3 * time.Second
	// prePauseSaveBudget bounds the writes of the copies before a pause or
	// stop. The flush that follows has the rest of vmm.FlushTimeout.
	prePauseSaveBudget = 5 * time.Second
)

func (m *Manager) eventFillDelay() time.Duration {
	if m.opts.EventFillDelay > 0 {
		return m.opts.EventFillDelay
	}
	return defaultEventFillDelay
}

// servingReporter reports whether the machine of a tenant runs and no pause
// or stop of it is in progress (implemented by *GuestDiscovery).
type servingReporter interface {
	Serving(tenantID string) bool
}

// serving reports whether a fill can read the guest of tenantID now.
func (m *Manager) serving(tenantID string) bool {
	s, ok := m.opts.Discovery.(servingReporter)
	return !ok || s.Serving(tenantID)
}

// fillItem is one session that a fill can read.
type fillItem struct {
	harness, nativeID string
	updated           time.Time
}

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

// eventSource returns the event reader of the fill, or false when the host
// keeps no copies.
func (m *Manager) eventSource() (EventSource, bool) {
	src, ok := m.opts.Discovery.(EventSource)
	return src, ok && m.opts.EventCache != nil
}

// beginFill marks a fill of tenantID as running. It reports false when one
// runs already: one fill per tenant runs at a time.
func (m *Manager) beginFill(tenantID string) bool {
	m.copies.mu.Lock()
	defer m.copies.mu.Unlock()
	if m.copies.filling[tenantID] {
		return false
	}
	if m.copies.filling == nil {
		m.copies.filling = map[string]bool{}
	}
	m.copies.filling[tenantID] = true
	return true
}

// endFill ends a fill and starts the fill of the sessions that a detach or
// session end added in the meantime.
func (m *Manager) endFill(tenantID string) {
	m.copies.mu.Lock()
	delete(m.copies.filling, tenantID)
	m.copies.mu.Unlock()
	m.fillPending(tenantID)
}

// fillAfterDiscovery starts a background fill after a fresh discovery (the
// machine runs). It reads the sessions whose copy is older than their
// transcript, newest first, one at a time and without a wake, and stops
// when the machine does not run. A non-nil keep first removes the copies
// of the sessions that keep does not have (pruneEventCopies).
func (m *Manager) fillAfterDiscovery(tenantID string, discovered map[string][]SessionMeta, keep map[string]bool) {
	src, ok := m.eventSource()
	if !ok || !m.beginFill(tenantID) {
		return
	}
	items := fillItems(discovered)
	go func() {
		defer m.endFill(tenantID)
		ctx, cancel := context.WithTimeout(context.Background(), eventFillBudget)
		defer cancel()
		if keep != nil {
			m.pruneEventCopies(ctx, tenantID, keep)
		}
		m.fillEvents(ctx, tenantID, "discovery", items, eventFillMax, eventFillMinAge, src)
	}()
}

// fillSoon asks for a fill of the copy of source after eventFillDelay. The
// manager calls it when a terminal detaches or a session ends: the session
// then has no terminal and maybe no watcher, so its copy can be stale. A
// copy that a hub wrote after the call is not read again.
func (m *Manager) fillSoon(tenantID, source string) {
	if _, ok := m.eventSource(); !ok {
		return
	}
	m.copies.mu.Lock()
	if m.copies.pending == nil {
		m.copies.pending = map[string]map[string]time.Time{}
	}
	if m.copies.pending[tenantID] == nil {
		m.copies.pending[tenantID] = map[string]time.Time{}
	}
	m.copies.pending[tenantID][source] = time.Now()
	m.copies.mu.Unlock()
	time.AfterFunc(m.eventFillDelay(), func() { m.fillPending(tenantID) })
}

// fillPending starts a background fill of the sessions that fillSoon added
// and whose delay is over, unless a fill of the tenant runs (endFill calls
// it again).
func (m *Manager) fillPending(tenantID string) {
	src, ok := m.eventSource()
	if !ok {
		return
	}
	due := time.Now().Add(-m.eventFillDelay())
	m.copies.mu.Lock()
	var items []fillItem
	for source, at := range m.copies.pending[tenantID] {
		if at.After(due) {
			continue
		}
		if name, nativeID, err := SplitSessionID(source); err == nil {
			items = append(items, fillItem{harness: name, nativeID: nativeID, updated: at})
		}
	}
	if len(items) == 0 || m.copies.filling[tenantID] {
		m.copies.mu.Unlock()
		return
	}
	for _, it := range items {
		delete(m.copies.pending[tenantID], SessionID(it.harness, it.nativeID))
	}
	if m.copies.filling == nil {
		m.copies.filling = map[string]bool{}
	}
	m.copies.filling[tenantID] = true
	m.copies.mu.Unlock()
	go func() {
		defer m.endFill(tenantID)
		ctx, cancel := context.WithTimeout(context.Background(), eventFillBudget)
		defer cancel()
		m.fillEvents(ctx, tenantID, "terminal end", items, eventFillMax, 0, src)
	}()
}

// SaveEventCopiesBeforePause writes the changed copies of the watched
// sessions of a tenant from memory before the host pauses or stops its
// machine (vmm.Options.Flush). It makes no guest call, so it does not delay
// the pause; it does nothing without an EventCache.
func (m *Manager) SaveEventCopiesBeforePause(ctx context.Context, tenantID string) {
	if m.opts.EventCache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, prePauseSaveBudget)
	defer cancel()
	start := time.Now()
	if n := m.saveHubs(ctx, tenantID); n > 0 {
		m.log.Info("stored the event copies of the watched sessions before a pause or stop",
			"sessions", n, "elapsed", time.Since(start).Round(time.Millisecond))
	}
}

// fillEvents reads and stores the events of the stale sessions of items: no
// copy, or a copy older than the transcript and at least minAge old. A
// watched session is left to its hub. It stops when the machine does not
// run. It returns the number of copies written.
func (m *Manager) fillEvents(ctx context.Context, tenantID, why string, items []fillItem, limit int, minAge time.Duration, src EventSource) int {
	if !m.serving(tenantID) {
		return 0
	}
	start := time.Now()
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
	tried, written := 0, 0
	defer func() {
		if tried > 0 {
			m.log.Info("stored the event copies of sessions without a watcher", "trigger", why,
				"sessions", tried, "written", written, "elapsed", time.Since(start).Round(time.Millisecond))
		}
	}()
	for _, it := range todo[:min(len(todo), limit)] {
		if ctx.Err() != nil || !m.serving(tenantID) {
			return written
		}
		tried++
		ok, err := m.fillOne(ctx, tenantID, it, src)
		switch {
		case errors.Is(err, ErrUnavailable), ctx.Err() != nil:
			// The machine does not run (or the time is over): stop.
			return written
		case ok:
			written++
		}
	}
	return written
}

// fillOne reads all events of one session without a wake and writes its
// copy. It reports whether it wrote the copy.
func (m *Manager) fillOne(ctx context.Context, tenantID string, it fillItem, src EventSource) (bool, error) {
	source := SessionID(it.harness, it.nativeID)
	gen := m.copies.gen(tenantID, source)
	var mu sync.Mutex
	l := newEventList()
	rctx, cancel := context.WithTimeout(ctx, eventFillReadTimeout)
	_, _, err := readPages(rctx, &mu, &l, 0, func(ctx context.Context, after int) (EventPage, error) {
		return src.Events(ctx, tenantID, it.harness, it.nativeID, after, false)
	})
	cancel()
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case errors.Is(err, ErrUnavailable), ctx.Err() != nil:
		return false, err
	case err != nil:
		m.log.Info("read the session events for the stored copy", "session", source, "err", err)
		return false, nil
	}
	if m.watched(tenantID, source) {
		return false, nil
	}
	if err := m.putEventCopy(ctx, tenantID, source, gen, l.list); err != nil {
		m.log.Warn("store the session events", "session", source, "err", err)
		return false, nil
	}
	return true, nil
}
