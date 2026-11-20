package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// MaxSessionEventsBytes bounds the stored events of one session: the sum of
// the JSON sizes of the events before compression. PutSessionEvents keeps
// the newest events that fit and cuts the oldest ones. An event that alone
// is larger than the bound is cut too (the harness bounds make one event
// much smaller: text 32 KiB, two images of 1 MiB).
const MaxSessionEventsBytes = 4 << 20

// maxSessionEventsInflated bounds the decompressed size on read, so that a
// damaged row cannot fill the memory.
const maxSessionEventsInflated = MaxSessionEventsBytes + 1<<20

// SessionEvents is the stored copy of the events of one session.
type SessionEvents struct {
	// Events are the JSON objects of the events, oldest first.
	Events []json.RawMessage
	// Cut is the number of older events that the size bound removed.
	Cut       int
	UpdatedAt time.Time
}

// SessionEventsStamp is the time of the stored copy of one session.
type SessionEventsStamp struct {
	Harness   string
	SessionID string
	UpdatedAt time.Time
}

// PutSessionEvents stores (or replaces) the copy of the events of a session.
// It keeps the newest events inside MaxSessionEventsBytes and compresses
// them (gzip).
func (s *Store) PutSessionEvents(ctx context.Context, tenantID, harness, sessionID string, events []json.RawMessage) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if harness == "" || sessionID == "" {
		return errors.New("store: put session events: harness and session ID are required")
	}
	kept, cut := newestWithin(events, MaxSessionEventsBytes)
	raw, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("store: put session events: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return fmt.Errorf("store: put session events: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("store: put session events: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO session_events_cache (tenant_id, harness, session_id, events_gz, event_count, cut_count, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, harness, session_id) DO UPDATE SET events_gz = excluded.events_gz,
	event_count = excluded.event_count, cut_count = excluded.cut_count, updated_at = excluded.updated_at`,
		tenantID, harness, sessionID, buf.Bytes(), len(kept), cut, s.timestamp())
	if err != nil {
		return fmt.Errorf("store: put session events: %w", err)
	}
	return nil
}

// newestWithin returns the newest events whose sizes add up to at most
// budget, and the number of events it cut.
func newestWithin(events []json.RawMessage, budget int) ([]json.RawMessage, int) {
	size, first := 0, len(events)
	for i := len(events) - 1; i >= 0; i-- {
		n := len(events[i]) + 1 // the comma of the array
		if size+n > budget {
			break
		}
		size += n
		first = i
	}
	return events[first:], first
}

// GetSessionEvents returns the stored copy of a session, or ErrNotFound.
func (s *Store) GetSessionEvents(ctx context.Context, tenantID, harness, sessionID string) (SessionEvents, error) {
	if err := checkTenant(tenantID); err != nil {
		return SessionEvents{}, err
	}
	var (
		blob    []byte
		cut     int
		updated int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT events_gz, cut_count, updated_at FROM session_events_cache WHERE tenant_id = ? AND harness = ? AND session_id = ?`,
		tenantID, harness, sessionID).Scan(&blob, &cut, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionEvents{}, ErrNotFound
	}
	if err != nil {
		return SessionEvents{}, fmt.Errorf("store: get session events: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return SessionEvents{}, fmt.Errorf("store: get session events: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxSessionEventsInflated+1))
	if err != nil {
		return SessionEvents{}, fmt.Errorf("store: get session events: %w", err)
	}
	if len(raw) > maxSessionEventsInflated {
		return SessionEvents{}, errors.New("store: get session events: the stored copy is too large")
	}
	out := SessionEvents{Cut: cut, UpdatedAt: fromMillis(updated)}
	if err := json.Unmarshal(raw, &out.Events); err != nil {
		return SessionEvents{}, fmt.Errorf("store: get session events: %w", err)
	}
	return out, nil
}

// DeleteSessionEvents removes the copy of a session; an absent one is no
// error.
func (s *Store) DeleteSessionEvents(ctx context.Context, tenantID, harness, sessionID string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_events_cache WHERE tenant_id = ? AND harness = ? AND session_id = ?`,
		tenantID, harness, sessionID); err != nil {
		return fmt.Errorf("store: delete session events: %w", err)
	}
	return nil
}

// ListSessionEventsStamps returns the time of every stored copy of a
// tenant (without the events).
func (s *Store) ListSessionEventsStamps(ctx context.Context, tenantID string) ([]SessionEventsStamp, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT harness, session_id, updated_at FROM session_events_cache WHERE tenant_id = ? ORDER BY harness, session_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list session events: %w", err)
	}
	defer rows.Close()
	var out []SessionEventsStamp
	for rows.Next() {
		var (
			st SessionEventsStamp
			at int64
		)
		if err := rows.Scan(&st.Harness, &st.SessionID, &at); err != nil {
			return nil, fmt.Errorf("store: list session events: %w", err)
		}
		st.UpdatedAt = fromMillis(at)
		out = append(out, st)
	}
	return out, rows.Err()
}
