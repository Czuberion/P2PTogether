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

// Snapshot returns a copy (!) of the currently stored segments in order
// oldest→newest together with the EXT‑X‑MEDIA‑SEQUENCE of the first slot.
func (rb *RingBuffer) Snapshot() (base uint32, segs []Segment) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	segs = make([]Segment, 0, rb.capacity)
	for i := 0; i < rb.capacity; i++ {
		idx := (rb.head + i) % rb.capacity
		s := rb.buf[idx]
		if s.Data == nil || s.Seq < rb.baseSeq {
			continue
		}
		segs = append(segs, s)
	}
	return rb.baseSeq, segs
}

// Get returns the payload for seq or nil if evicted / not yet seen.
func (rb *RingBuffer) Get(seq uint32) []byte {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if seq < rb.baseSeq {
		return nil // too old
	}
	delta := seq - rb.baseSeq
	if int(delta) >= rb.capacity {
		return nil // not written yet OR wrapped
	}
	return rb.buf[(rb.head+int(delta))%rb.capacity].Data
}
