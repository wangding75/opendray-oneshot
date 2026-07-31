package session

import "sync"

// RingBuffer is a fixed-capacity circular byte buffer used to hold a
// session's recent stdout for replay. Writes never block; once full,
// each new byte overwrites the oldest. The buffer is safe for
// concurrent Write/Snapshot use.
//
// `written` is a monotonic counter of total bytes ever written; clients
// pass it back as `since` on reconnect to receive only the bytes they
// missed.
type RingBuffer struct {
	mu      sync.Mutex
	buf     []byte
	cap     int
	pos     int
	full    bool
	written int64
}

// Replay is the result of SnapshotSince. Start is the absolute offset of
// Bytes[0]; if Start > the requested `since`, the client lagged the
// buffer's capacity and (Start - since) bytes were dropped. Written is
// the new cursor the client should pass on the next call.
type Replay struct {
	Bytes   []byte
	Start   int64
	Written int64
}

func NewRing(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{buf: make([]byte, capacity), cap: capacity}
}

// Write implements io.Writer. Always returns len(p), nil — never errors.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	r.written += int64(n)
	if n == 0 {
		return 0, nil
	}
	if n >= r.cap {
		copy(r.buf, p[n-r.cap:])
		r.pos = 0
		r.full = true
		return n, nil
	}
	end := r.pos + n
	if end <= r.cap {
		copy(r.buf[r.pos:], p)
		r.pos = end
		if r.pos == r.cap {
			r.pos = 0
			r.full = true
		}
	} else {
		first := r.cap - r.pos
		copy(r.buf[r.pos:], p[:first])
		copy(r.buf, p[first:])
		r.pos = n - first
		r.full = true
	}
	return n, nil
}

// Snapshot returns the current buffer contents in chronological order.
// Equivalent to SnapshotSince(0).Bytes.
func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// SnapshotSince returns the bytes written after offset `since`. If the
// caller lagged the ring's capacity, Replay.Start will exceed `since`
// and (Start - since) bytes have been dropped.
func (r *RingBuffer) SnapshotSince(since int64) Replay {
	r.mu.Lock()
	defer r.mu.Unlock()

	written := r.written
	var sizeNow int
	if r.full {
		sizeNow = r.cap
	} else {
		sizeNow = r.pos
	}
	minAvail := written - int64(sizeNow)

	start := since
	if start < minAvail {
		start = minAvail
	}
	if start >= written {
		return Replay{Start: start, Written: written}
	}

	all := r.snapshotLocked()
	skip := start - minAvail
	out := make([]byte, int64(len(all))-skip)
	copy(out, all[skip:])
	return Replay{Bytes: out, Start: start, Written: written}
}

func (r *RingBuffer) snapshotLocked() []byte {
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	out := make([]byte, r.cap)
	copy(out, r.buf[r.pos:])
	copy(out[r.cap-r.pos:], r.buf[:r.pos])
	return out
}

func (r *RingBuffer) BytesWritten() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written
}
