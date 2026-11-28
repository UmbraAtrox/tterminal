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

type WindowMode string

const (
	WindowTail  WindowMode = "tail"
	WindowDelta WindowMode = "delta"
	WindowAll   WindowMode = "all"
)

type WindowResult struct {
	Data             []byte
	RangeStart       int64
	Cursor           int64
	Next             int64
	Base             int64
	AvailableBytes   int64
	SkippedBytes     int64
	RingDroppedBytes int64
	Truncated        bool
	TruncationReason string
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

func (r *Ring) Limit() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limit
}

func (r *Ring) ReadWindow(requested int64, end int64, mode WindowMode, maxBytes int, maxLines int) WindowResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	base := r.base
	next := r.next
	if end <= 0 || end > next {
		end = next
	}
	if end < base {
		end = base
	}

	rangeStart := requested
	if rangeStart < 0 {
		rangeStart = base
	}
	start := rangeStart
	if start < base {
		start = base
	}
	if start > end {
		start = end
	}

	if mode == WindowAll {
		start = base
		if start > end {
			start = end
		}
	}

	result := WindowResult{
		RangeStart:       rangeStart,
		Cursor:           start,
		Next:             end,
		Base:             base,
		AvailableBytes:   end - start,
		RingDroppedBytes: max64(base-rangeStart, 0),
	}

	startIndex := int(start - base)
	endIndex := int(end - base)
	if startIndex < 0 {
		startIndex = 0
	}
	if endIndex > len(r.data) {
		endIndex = len(r.data)
	}
	if endIndex < startIndex {
		endIndex = startIndex
	}

	if mode == WindowTail {
		tailStart := tailStartIndex(r.data, startIndex, endIndex, maxBytes, maxLines)
		if tailStart > startIndex {
			result.SkippedBytes = int64(tailStart - startIndex)
			result.Cursor = base + int64(tailStart)
			startIndex = tailStart
		}
	}

	result.Data = append([]byte(nil), r.data[startIndex:endIndex]...)
	if result.SkippedBytes > 0 || result.RingDroppedBytes > 0 {
		result.Truncated = true
		switch {
		case result.SkippedBytes > 0 && result.RingDroppedBytes > 0:
			result.TruncationReason = "both"
		case result.SkippedBytes > 0:
			result.TruncationReason = "cap"
		default:
			result.TruncationReason = "ring"
		}
	}
	return result
}

func tailStartIndex(data []byte, start int, end int, maxBytes int, maxLines int) int {
	startIndex := start
	if maxBytes > 0 && end-start > maxBytes {
		startIndex = end - maxBytes
		if startIndex < start {
			startIndex = start
		}
	}
	if maxLines > 0 {
		lines := 0
		for i := end - 1; i >= start; i-- {
			if data[i] == '\n' {
				lines++
				if lines > maxLines {
					if i+1 > startIndex {
						startIndex = i + 1
					}
					break
				}
			}
		}
	}
	if startIndex < start {
		startIndex = start
	}
	if startIndex > end {
		startIndex = end
	}
	return startIndex
}

func max64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
