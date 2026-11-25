package buffer

import (
	"context"
	"sync"
)

type ReadResult struct {
	Data      []byte
	Cursor    int64
	Next      int64
	Truncated bool
}

type Ring struct {
	mu      sync.Mutex
	data    []byte
	base    int64
	next    int64
	limit   int
	closed  bool
	changed chan struct{}
}

func New(limit int) *Ring {
	if limit <= 0 {
		limit = 1 << 20
	}
	return &Ring{
		data:    make([]byte, 0, limit/4+1),
		limit:   limit,
		changed: make(chan struct{}),
	}
}

func (r *Ring) Write(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	r.mu.Lock()
	r.data = append(r.data, p...)
	if len(r.data) > r.limit {
		drop := len(r.data) - r.limit
		copy(r.data, r.data[drop:])
		r.data = r.data[:r.limit]
		r.base += int64(drop)
	}
	r.next += int64(len(p))
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
	return len(p)
}

func (r *Ring) Read(cursor int64, maxBytes int) ReadResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	result := ReadResult{Cursor: cursor, Next: r.next}
	if cursor < r.base {
		result.Cursor = r.base
		result.Truncated = true
	}
	if result.Cursor > r.next {
		result.Cursor = r.next
	}
	if r.len() == 0 {
		return result
	}
	start := int(result.Cursor - r.base)
	if start < 0 {
		start = 0
	}
	if start >= len(r.data) {
		return result
	}
	end := start + maxBytes
	if end > len(r.data) {
		end = len(r.data)
	}
	if end < start {
		end = start
	}
	result.Data = append([]byte(nil), r.data[start:end]...)
	result.Next = r.base + int64(end)
	return result
}

func (r *Ring) len() int {
	return len(r.data)
}

func (r *Ring) Range() (int64, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base, r.next
}

func (r *Ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.data...)
}

func (r *Ring) SetLimit(limit int) {
	if limit <= 0 {
		return
	}
	r.mu.Lock()
	if len(r.data) > limit {
		drop := len(r.data) - limit
		copy(r.data, r.data[drop:])
		r.data = r.data[:limit]
		r.base += int64(drop)
	}
	r.limit = limit
	r.mu.Unlock()
}

func (r *Ring) Clear() {
	r.mu.Lock()
	r.data = r.data[:0]
	r.base = r.next
	r.mu.Unlock()
}

func (r *Ring) Wait(ctx context.Context, cursor int64) error {
	for {
		r.mu.Lock()
		if r.closed || r.next > cursor {
			r.mu.Unlock()
			return nil
		}
		ch := r.changed
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Ring) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.changed)
	}
	r.mu.Unlock()
}
