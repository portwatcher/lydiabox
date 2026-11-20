package httpx

import (
	"context"
	"sync/atomic"
)

// RetryAfterStarting is the Retry-After value (seconds) of a 503 response
// to a request that stopped its wait for a VM start while the start
// continues. A client sends the request again after this time.
const RetryAfterStarting = 5

// StartingNote records that a request stopped its wait for a VM start
// while the start continues (the request has a shorter bound than the
// start). The API's request log puts one note in the context of each
// request; the VM controller sets it (NoteVMStarting). A 503 response of
// such a request then gets "Retry-After: 5" and is logged as an expected
// state, not a fault.
type StartingNote struct{ set atomic.Bool }

// Starting reports whether NoteVMStarting was called for the request.
func (n *StartingNote) Starting() bool { return n != nil && n.set.Load() }

type startingKey struct{}

// WithStartingNote returns a context that carries a new note.
func WithStartingNote(ctx context.Context) (context.Context, *StartingNote) {
	n := &StartingNote{}
	return context.WithValue(ctx, startingKey{}, n), n
}

// NoteVMStarting sets the note of ctx (a request context or a context
// made from it). Without a note it does nothing.
func NoteVMStarting(ctx context.Context) {
	if n, ok := ctx.Value(startingKey{}).(*StartingNote); ok {
		n.set.Store(true)
	}
}
