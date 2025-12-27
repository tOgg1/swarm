package runner

import "sync"

// LineRing stores the last N lines of output.
type LineRing struct {
	mu    sync.Mutex
	size  int
	lines []string
	next  int
	full  bool
}

// NewLineRing returns a ring buffer sized for the provided line count.
func NewLineRing(size int) *LineRing {
	if size <= 0 {
		size = 1
	}
	return &LineRing{
		size:  size,
		lines: make([]string, size),
	}
}

// Add stores a line in the ring buffer.
func (r *LineRing) Add(line string) {
	if r == nil {
		return
	}
	if r.size <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.lines[r.next] = line
	r.next++
	if r.next >= r.size {
		r.next = 0
		r.full = true
	}
}

// Snapshot returns the buffered lines in chronological order.
func (r *LineRing) Snapshot() []string {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		out := make([]string, r.next)
		copy(out, r.lines[:r.next])
		return out
	}

	out := make([]string, r.size)
	copy(out, r.lines[r.next:])
	copy(out[r.size-r.next:], r.lines[:r.next])
	return out
}
