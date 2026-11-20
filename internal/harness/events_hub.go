package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// States of a session in the event stream.
const (
	// StateWorking: the terminal wrote output in the last seconds.
	StateWorking = "working"
	// StateIdle: the terminal runs and is quiet.
	StateIdle = "idle"
	// StateAttention: the terminal is quiet and shows an approval prompt.
	StateAttention = "attention"
	// StateSetup: before the first event, the terminal shows a first-run
	// screen (terms, folder trust, sign-in method) that only the user can
	// answer. The program does not read prompts until then.
	StateSetup = "setup"
	// StateOffline: the session has no running terminal.
	StateOffline = "offline"
)

// EventStatus is the state of a session in the event stream.
type EventStatus struct {
	// SessionID is the API ID that the session has now (a provisional
	// session gets its native ID after the harness wrote it).
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
	Live      bool   `json:"live"`
	// Synced reports that the events of the transcript were read once.
	Synced bool `json:"synced"`
	// Cached reports that the events are the copy that the host stored
	// (provider storage mode) and the guest was not read yet: the client
	// sees them at once while the machine starts or resumes.
	Cached bool `json:"cached,omitempty"`
	// Prompt is the approval prompt or setup screen line that the
	// terminal shows.
	Prompt string `json:"prompt,omitempty"`
	// Error is the last read error, or why the ACP agent ended.
	Error string `json:"error,omitempty"`
	// Engine is EngineACP while the bridge runs the session.
	Engine string `json:"engine,omitempty"`
	// Agent, Mode, and Queued describe the ACP agent.
	Agent  string `json:"agent,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Queued int    `json:"queued,omitempty"`
}

// EventUpdate is one message to an event stream client. Reset replaces
// every event the client has; otherwise the events replace the events of
// the same key or are new.
type EventUpdate struct {
	Reset  bool
	Events []Event
	Status *EventStatus
}

// EventSub receives the updates of a session. C is closed when the client
// fell too far behind.
type EventSub struct {
	C    chan EventUpdate
	once sync.Once
}

func (s *EventSub) close() { s.once.Do(func() { close(s.C) }) }

// eventPoll is the poll interval of a live session; output in the last two
// intervals means StateWorking.
func (m *Manager) eventPoll() time.Duration {
	if m.opts.EventPoll > 0 {
		return m.opts.EventPoll
	}
	return time.Second
}

const (
	// eventTailResend is how many of the last events every poll reads
	// again, so that an event that changed in place (a streamed text, an
	// opencode tool that finished) reaches the clients.
	eventTailResend = 32
	eventSubBuffer  = 64
	maxPagesPerPoll = 64
)

type eventHub struct {
	m        *Manager
	tenantID string
	id       string
	cancel   context.CancelFunc

	mu     sync.Mutex
	ev     eventList
	source string
	status EventStatus
	subs   map[*EventSub]struct{}
	// ended is the last status of an ACP bridge that ended, so that its
	// error stays visible after the session is offline.
	ended EventStatus
	// cached reports that ev is the stored copy and the guest was not
	// read yet (events_cache.go).
	cached bool
	// copyOf is the session whose copy the hub writes ("" for none), gen
	// the cache generation of that session when the hub took it, and
	// dirty that ev changed after the last write.
	copyOf string
	gen    uint64
	dirty  bool
	// saveMu serializes the writes of the copy.
	saveMu sync.Mutex
}

// eventList is the ordered event list of a hub. cursor is the index after
// the last event read from the source.
type eventList struct {
	list   []Event
	index  map[string]int
	sums   map[string]string
	cursor int
}

func newEventList() eventList {
	return eventList{index: map[string]int{}, sums: map[string]string{}}
}

// SubscribeEvents attaches a client to the event stream of a session. The
// first update holds every event read so far (Reset) and the status. The
// caller calls the returned func to detach.
func (m *Manager) SubscribeEvents(tenantID, id string) (*EventSub, EventUpdate, func(), error) {
	h, _, err := SplitSessionID(id)
	if err != nil {
		return nil, EventUpdate{}, nil, err
	}
	if h == Shell {
		return nil, EventUpdate{}, nil, fmt.Errorf("%w: a shell session has no events", ErrNotFound)
	}
	if _, ok := m.adapters[h]; !ok {
		return nil, EventUpdate{}, nil, fmt.Errorf("%w: unknown harness %q", ErrNotFound, h)
	}
	key := liveKey(tenantID, id)
	m.hubsMu.Lock()
	hub := m.hubs[key]
	m.hubsMu.Unlock()
	var pre preload
	if hub == nil {
		// The stored copy is read before the first read of the guest,
		// which can wait for the machine to start or resume.
		pre = m.preloadEvents(tenantID, id)
	}
	m.hubsMu.Lock()
	if m.hubs == nil {
		m.hubs = map[string]*eventHub{}
	}
	hub = m.hubs[key]
	if hub == nil {
		ctx, cancel := context.WithCancel(context.Background())
		hub = &eventHub{m: m, tenantID: tenantID, id: id, cancel: cancel, ev: newEventList(),
			subs: map[*EventSub]struct{}{}, status: EventStatus{SessionID: id, State: StateOffline}}
		hub.takePreload(pre)
		m.hubs[key] = hub
		go hub.run(ctx)
		if m.opts.EventCache != nil {
			go hub.saver(ctx)
		}
	}
	sub := &EventSub{C: make(chan EventUpdate, eventSubBuffer)}
	hub.mu.Lock()
	hub.subs[sub] = struct{}{}
	status := hub.status
	first := EventUpdate{Reset: true, Events: append([]Event{}, hub.ev.list...), Status: &status}
	hub.mu.Unlock()
	m.hubsMu.Unlock()
	return sub, first, func() { m.unsubscribeEvents(key, hub, sub) }, nil
}

// hasEvents reports whether a watched event stream of the session has
// events. A session that nobody watches counts as one without events.
func (m *Manager) hasEvents(tenantID, id string) bool {
	m.hubsMu.Lock()
	hub := m.hubs[liveKey(tenantID, id)]
	m.hubsMu.Unlock()
	if hub == nil {
		return false
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.ev.list) > 0
}

func (m *Manager) unsubscribeEvents(key string, hub *eventHub, sub *EventSub) {
	m.hubsMu.Lock()
	defer m.hubsMu.Unlock()
	hub.mu.Lock()
	delete(hub.subs, sub)
	sub.close()
	empty := len(hub.subs) == 0
	hub.mu.Unlock()
	if empty && m.hubs[key] == hub {
		delete(m.hubs, key)
		hub.cancel()
	}
}

// liveView is a copy of the fields of a live session.
type liveView struct {
	harness, nativeID, current, engine string
	provisional                        bool
	room                               *Room
}

// liveInfo reads the fields of the live session of id under the manager
// lock.
func (m *Manager) liveInfo(tenantID, id string) (liveView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := liveKey(tenantID, id)
	if to, found := m.aliases[k]; found {
		k = to
	}
	ls := m.live[k]
	if ls == nil {
		return liveView{}, false
	}
	return liveView{harness: ls.harness, nativeID: ls.nativeID, current: SessionID(ls.harness, ls.nativeID),
		engine: ls.engine, provisional: ls.provisional, room: ls.room}, true
}

func (h *eventHub) run(ctx context.Context) {
	for tick := 0; ; tick++ {
		if lv, ok := h.m.liveInfo(h.tenantID, h.id); ok && lv.engine == EngineACP && lv.room != nil {
			// The bridge streams the events; the transcript is read again
			// after it ends.
			h.followACP(ctx, lv)
			if ctx.Err() != nil {
				return
			}
			tick = 0
		}
		live := h.poll(ctx, tick)
		wait := 4 * h.m.eventPoll()
		if live {
			wait = h.m.eventPoll()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// poll reads new events and the terminal state once and sends the changes.
// It reports whether the session has a live terminal.
func (h *eventHub) poll(ctx context.Context, tick int) bool {
	m := h.m
	lv, live := m.liveInfo(h.tenantID, h.id)
	if live && lv.provisional && tick%3 == 0 {
		// The session list adopts the native ID that the harness wrote.
		_, _ = m.ListSessions(ctx, h.tenantID, Filter{Harness: lv.harness})
		lv, live = m.liveInfo(h.tenantID, h.id)
	}
	name, nativeID, current, provisional, room := lv.harness, lv.nativeID, lv.current, lv.provisional, lv.room
	if !live {
		name, nativeID, _ = SplitSessionID(h.id)
		current = h.id
	}
	source := ""
	if !provisional && nativeID != "" {
		source = SessionID(name, nativeID)
	}

	h.mu.Lock()
	reset := source != h.source
	if reset {
		h.clearLocked()
		h.source = source
	}
	cached := h.cached
	h.mu.Unlock()

	var changed []Event
	status := EventStatus{SessionID: current, Live: live, State: StateOffline}
	if source == "" {
		status.Synced = true
	} else {
		// After a preload the guest events are read into a new list, and
		// the clients keep the stored copy until the read is complete.
		target := &h.ev
		fresh := newEventList()
		if cached {
			target = &fresh
		}
		var err error
		var again bool
		changed, again, err = h.read(ctx, target, name, nativeID, tick == 0)
		reset = reset || again
		switch {
		case err == nil, errors.Is(err, ErrNotFound):
			status.Synced = true
			if cached {
				again, changed = h.adoptFresh(fresh)
				reset = reset || again
				if errors.Is(err, ErrNotFound) {
					h.m.dropEventCopy(h.tenantID, source)
				}
			}
			if err == nil && (reset || len(changed) > 0) {
				h.markDirty(source)
			}
		case ctx.Err() != nil:
			return live
		default:
			status.Error = err.Error()
		}
	}
	h.mu.Lock()
	status.Cached = h.cached
	h.mu.Unlock()
	switch {
	case live:
		h.ended = EventStatus{}
	case h.ended.Error != "":
		status.Engine, status.Error = EngineACP, h.ended.Error
	}
	if live && room != nil {
		act := room.Activity()
		quiet := act.LastOutput.IsZero() || time.Since(act.LastOutput) > 2*m.eventPoll()
		h.mu.Lock()
		first := len(h.ev.list) == 0
		h.mu.Unlock()
		switch {
		case first && act.Setup != "":
			status.State, status.Prompt = StateSetup, act.Setup
		case quiet && act.Prompt != "":
			status.State, status.Prompt = StateAttention, act.Prompt
		case !quiet:
			status.State = StateWorking
		default:
			status.State = StateIdle
		}
	}
	h.publish(reset, changed, status)
	return live
}

// clearLocked drops the events of the hub (also a preloaded copy).
func (h *eventHub) clearLocked() {
	h.ev = newEventList()
	h.cached = false
}

func (h *eventHub) upsertLocked(evs []Event) []Event { return h.ev.upsert(evs) }

// read reads the events after the cursor of l (and the last
// eventTailResend again) into l, under the hub lock. again reports that the
// source shrank and was read from the start.
func (h *eventHub) read(ctx context.Context, l *eventList, name, nativeID string, wake bool) (changed []Event, again bool, err error) {
	src, ok := h.m.opts.Discovery.(EventSource)
	if !ok {
		return nil, false, fmt.Errorf("%w: the session store has no event reader", ErrUnavailable)
	}
	h.mu.Lock()
	after := max(0, l.cursor-eventTailResend)
	h.mu.Unlock()
	return readPages(ctx, &h.mu, l, after, func(ctx context.Context, after int) (EventPage, error) {
		return src.Events(ctx, h.tenantID, name, nativeID, after, wake)
	})
}

// readPages reads pages from after into l (under mu) until the end of the
// source or maxPagesPerPoll pages.
func readPages(ctx context.Context, mu *sync.Mutex, l *eventList, after int,
	next func(ctx context.Context, after int) (EventPage, error)) (changed []Event, again bool, err error) {
	for range maxPagesPerPoll {
		page, err := next(ctx, after)
		if err != nil {
			return changed, again, err
		}
		mu.Lock()
		if page.Total < l.cursor && !again {
			*l = newEventList()
			mu.Unlock()
			again, after, changed = true, 0, nil
			continue
		}
		changed = append(changed, l.upsert(page.Events)...)
		l.cursor = max(l.cursor, page.Next)
		mu.Unlock()
		if page.Next >= page.Total || len(page.Events) == 0 {
			break
		}
		after = page.Next
	}
	return changed, again, nil
}

// upsert adds new events and replaces changed ones; it returns them.
func (l *eventList) upsert(evs []Event) []Event {
	var out []Event
	for _, e := range evs {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		sum := string(b)
		if i, ok := l.index[e.Key]; ok {
			if l.sums[e.Key] == sum {
				continue
			}
			l.list[i] = e
		} else {
			l.index[e.Key] = len(l.list)
			l.list = append(l.list, e)
		}
		l.sums[e.Key] = sum
		out = append(out, e)
	}
	return out
}

func (h *eventHub) publish(reset bool, changed []Event, status EventStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	statusChanged := status != h.status
	h.status = status
	var u EventUpdate
	switch {
	case reset:
		u = EventUpdate{Reset: true, Events: append([]Event{}, h.ev.list...), Status: &status}
	case len(changed) > 0 || statusChanged:
		u = EventUpdate{Events: changed}
		if statusChanged {
			u.Status = &status
		}
	default:
		return
	}
	for s := range h.subs {
		select {
		case s.C <- u:
		default:
			// The client cannot keep up; it reconnects and gets a reset.
			delete(h.subs, s)
			s.close()
		}
	}
}
