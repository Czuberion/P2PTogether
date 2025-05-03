// internal/media/ring_buffer.go
package media

import (
	"sync"
	"time"
)

// SegmentDuration is hard‑coded 2 s for LAN‑MVP; later we’ll derive it from
// ffmpeg cfg or #EXTINF.
const SegmentDuration = 2 * time.Second

// A Segment is a complete 2‑second MPEG‑TS chunk.
type Segment struct {
	Seq  uint32    // monotonically increasing media‑sequence
	Data []byte    // raw .ts payload
	PTS  float64   // start PTS in seconds (for EXT‑X‑DISCONTINUITY later)
	Time time.Time // arrival → garbage‑collection anchor
}

// RingBuffer keeps the last N seconds of segments in RAM.
// It is safe for concurrent readers & writers.
type RingBuffer struct {
	mu       sync.RWMutex
	capacity int              // #segments == window/2s
	baseSeq  uint32           // sequence of the *oldest* slot
	nextSeq  uint32           // next seq to hand out, monotonic ^
	buf      []Segment        // fixed slice; len==capacity
	head     int              // write cursor   (0…capacity‑1)
	now      func() time.Time // injectable clock for tests
}

// NewRingBuffer returns a buffer that holds window seconds of video.
// window MUST be a multiple of 2 s. Example: NewRingBuffer(120) → 60 slots.
func NewRingBuffer(windowSeconds int) *RingBuffer {
	if windowSeconds%2 != 0 || windowSeconds <= 0 {
		panic("ring_buffer: windowSeconds must be a positive multiple of 2")
	}
	return &RingBuffer{
		capacity: windowSeconds / int(SegmentDuration.Seconds()),
		buf:      make([]Segment, windowSeconds/int(SegmentDuration.Seconds())),
		now:      time.Now,
	}
}

// Len returns how many segments are currently stored (could be less than capacity).
func (rb *RingBuffer) Len() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if rb.nextSeq <= rb.baseSeq {
		return 0
	}
	count := int(rb.nextSeq - rb.baseSeq)
	if count > rb.capacity {
		return rb.capacity
	}
	return count
}

// idxFor converts a sequence number into a buffer index, assuming seq ∈ [baseSeq, nextSeq).
// Returns -1 if the seq is out of range.
func (rb *RingBuffer) idxFor(seq uint32) int {
	if seq < rb.baseSeq || seq >= rb.nextSeq {
		return -1
	}
	// distance behind head:
	off := rb.nextSeq - seq
	// head points at next write slot, so back up off steps:
	idx := (rb.head - int(off) + rb.capacity) % rb.capacity
	return idx
}

func (rb *RingBuffer) Write(data []byte, pts float64) uint32 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	seq := rb.nextSeq
	rb.nextSeq++

	rb.buf[rb.head] = Segment{Seq: seq, Data: data, PTS: pts, Time: rb.now()}
	rb.head = (rb.head + 1) % rb.capacity

	// Re‑compute the first live sequence: everything older than capacity is gone.
	if rb.nextSeq > uint32(rb.capacity) {
		rb.baseSeq = rb.nextSeq - uint32(rb.capacity)
	}
	return seq
}

// Snapshot returns a slice of live segments (oldest→newest) together with the
// EXT‑X‑MEDIA‑SEQUENCE of the first slot.
func (rb *RingBuffer) Snapshot() (base uint32, segs []Segment) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.nextSeq == rb.baseSeq { // buffer empty
		return rb.baseSeq, nil
	}

	count := rb.nextSeq - rb.baseSeq
	segs = make([]Segment, 0, count)
	for seq := rb.baseSeq; seq < rb.nextSeq; seq++ {
		if idx := rb.idxFor(seq); idx >= 0 {
			segs = append(segs, rb.buf[idx])
		}
	}
	return rb.baseSeq, segs
}

// Get returns the raw payload for a given sequence, or nil if it's out of range.
func (rb *RingBuffer) Get(seq uint32) []byte {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if idx := rb.idxFor(seq); idx >= 0 {
		return rb.buf[idx].Data
	}
	return nil
}

// WriteAt writes a segment with an explicit sequence number. It
// advances nextSeq if needed and evicts old entries exactly
// like Write does.
func (rb *RingBuffer) WriteAt(seq uint32, data []byte, pts float64) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// place the segment at the current head
	rb.buf[rb.head] = Segment{
		Seq:  seq,
		Data: data,
		PTS:  pts,
		Time: rb.now(),
	}
	rb.head = (rb.head + 1) % rb.capacity

	// keep nextSeq monotonic
	if seq >= rb.nextSeq {
		rb.nextSeq = seq + 1
	}

	// evict anything older than capacity
	if rb.nextSeq > uint32(rb.capacity) {
		rb.baseSeq = rb.nextSeq - uint32(rb.capacity)
	}
}
