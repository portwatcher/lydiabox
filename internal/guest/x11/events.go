package x11

import (
	"encoding/json"
	"sync"
	"time"
)

// AgentAction is the agent_action event of docs/api.md section 9. Fields
// that do not apply stay zero.
type AgentAction struct {
	Type      string    `json:"type"`
	Action    string    `json:"action"`
	X         int       `json:"x"`
	Y         int       `json:"y"`
	FromX     int       `json:"fromX,omitempty"`
	FromY     int       `json:"fromY,omitempty"`
	Button    string    `json:"button"`
	Double    bool      `json:"double"`
	Text      string    `json:"text"`
	Keys      string    `json:"keys"`
	DX        int       `json:"dx"`
	DY        int       `json:"dy"`
	Tool      string    `json:"tool"`
	Timestamp time.Time `json:"timestamp"`
}

// ModeEvent is the mode event of docs/api.md section 9.
type ModeEvent struct {
	Type      string    `json:"type"`
	Mode      string    `json:"mode"`
	ChangedBy string    `json:"changedBy"`
	ChangedAt time.Time `json:"changedAt"`
}

// Hub fans desktop events out to subscribers (the host event stream). A
// subscriber that falls behind loses events instead of blocking the agent.
type Hub struct {
	mu   sync.Mutex
	subs map[chan json.RawMessage]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[chan json.RawMessage]struct{}{}} }

// Subscribe returns an event channel and a cancel function that closes it.
func (h *Hub) Subscribe() (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// Publish encodes v and delivers it to every subscriber.
func (h *Hub) Publish(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
		}
	}
}
