package ptymanager

// DefaultRingSize is the replay buffer kept per PTY (1 MiB).
const DefaultRingSize = 1 << 20

// Ring is a fixed-size byte ring that keeps the most recent output. It is
// not safe for concurrent use; Session guards it.
type Ring struct {
	buf   []byte
	start int // index of the oldest byte
	n     int // bytes stored
	total int64
}

// NewRing returns a ring that keeps the last size bytes.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = DefaultRingSize
	}
	return &Ring{buf: make([]byte, size)}
}

// Write appends p, overwriting the oldest bytes when full.
func (r *Ring) Write(p []byte) {
	r.total += int64(len(p))
	size := len(r.buf)
	if len(p) >= size {
		copy(r.buf, p[len(p)-size:])
		r.start, r.n = 0, size
		return
	}
	end := (r.start + r.n) % size
	first := copy(r.buf[end:], p)
	copy(r.buf, p[first:])
	r.n += len(p)
	if r.n > size {
		r.start = (r.start + r.n - size) % size
		r.n = size
	}
}

// Bytes returns a copy of the stored bytes, oldest first.
func (r *Ring) Bytes() []byte {
	out := make([]byte, r.n)
	first := copy(out, r.buf[r.start:min(r.start+r.n, len(r.buf))])
	copy(out[first:], r.buf[:r.n-first])
	return out
}

// Len returns the number of stored bytes.
func (r *Ring) Len() int { return r.n }

// Total returns the number of bytes ever written.
func (r *Ring) Total() int64 { return r.total }
