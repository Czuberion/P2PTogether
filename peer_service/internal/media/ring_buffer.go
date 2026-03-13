// internal/media/ring_buffer.go
package media

import (
	"log"
	"sync"
	"time"
)

// SegmentDuration is hard‑coded 2 s for LAN‑MVP; later we’ll derive it from
// ffmpeg cfg or #EXTINF.
const SegmentDuration = 2 * time.Second

// A Segment is a complete 2‑second MPEG‑TS chunk.
type Segment struct {
	Seq            uint32    // monotonically increasing media‑sequence
	Data           []byte    // raw .ts payload
	PTS            float64   // start PTS in seconds (for EXT‑X‑DISCONTINUITY later)
	Time           time.Time // arrival → garbage‑collection anchor
	ActualDuration float64   // Precise duration of this segment in seconds
}

// SegmentEvent is used to notify about new segments.
type SegmentEvent struct {
	Seq uint32
}

// RingBuffer keeps the last N seconds of segments in RAM.
// It is safe for concurrent readers & writers.
type RingBuffer struct {
	mu               sync.RWMutex
	capacity         int               // #segments == window/2s
	baseSeq          uint32            // sequence of the *oldest* slot
	nextSeq          uint32            // next seq to hand out, monotonic ^
	buf              []Segment         // fixed slice; len==capacity
	head             int               // write cursor   (0…capacity‑1)
	now              func() time.Time  // injectable clock for tests
	segmentEventChan chan SegmentEvent // Channel to send notifications
	publishEvents    bool              // Flag to control event publishing
}

// NewRingBuffer returns a buffer that holds window seconds of video.
// window MUST be a multiple of 2 s. Example: NewRingBuffer(120) → 60 slots.
func NewRingBuffer(windowSeconds int) *RingBuffer {
	if windowSeconds%(int(SegmentDuration.Seconds())) != 0 || windowSeconds <= 0 {
		panic("ring_buffer: windowSeconds must be a positive multiple of 2")
	}
	return &RingBuffer{
		capacity:         windowSeconds / int(SegmentDuration.Seconds()),
		buf:              make([]Segment, windowSeconds/int(SegmentDuration.Seconds())),
		now:              time.Now,
		segmentEventChan: make(chan SegmentEvent, 10), // Buffered channel, adjust size as needed
		publishEvents:    true,                        // Default to true
	}
}

// GetSegmentEventChannel returns the read-only channel for segment events.
func (rb *RingBuffer) GetSegmentEventChannel() <-chan SegmentEvent {
	return rb.segmentEventChan
}

// SetPublishEvents enables or disables publishing of segment events.
func (rb *RingBuffer) SetPublishEvents(publish bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.publishEvents = publish
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
	idx := int(seq % uint32(rb.capacity))
	if rb.buf[idx].Seq != seq || len(rb.buf[idx].Data) == 0 {
		return -1
	}
	return idx
}

func (rb *RingBuffer) Write(data []byte, pts float64, actualDuration float64) uint32 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	seq := rb.nextSeq
	idx := int(seq % uint32(rb.capacity))
	rb.buf[idx] = Segment{Seq: seq, Data: data, PTS: pts, Time: rb.now(), ActualDuration: actualDuration}
	rb.nextSeq++
	rb.head = int(rb.nextSeq % uint32(rb.capacity))

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

	log.Printf("[RingBuffer.Get] Requesting seq=%d. Current baseSeq=%d, nextSeq=%d", seq, rb.baseSeq, rb.nextSeq)
	if idx := rb.idxFor(seq); idx >= 0 {
		log.Printf("[RingBuffer.Get] Found seq=%d at index=%d. Data len: %d", seq, idx, len(rb.buf[idx].Data))
		return rb.buf[idx].Data
	}
	log.Printf("[RingBuffer.Get] seq=%d not found (out of range or not yet written).", seq)
	return nil
}

// WriteAt writes a segment with an explicit sequence number. It
// advances nextSeq if needed and evicts old entries exactly
// like Write does.
func (rb *RingBuffer) WriteAt(seq uint32, data []byte, pts float64, actualDuration float64) {
	rb.mu.Lock()

	if rb.capacity <= 0 {
		rb.mu.Unlock()
		return
	}

	// Keep nextSeq monotonic first so base/eviction math is consistent.
	if seq >= rb.nextSeq {
		rb.nextSeq = seq + 1
	}

	// Evict anything older than capacity.
	if rb.nextSeq > uint32(rb.capacity) {
		rb.baseSeq = rb.nextSeq - uint32(rb.capacity)
	}

	// Too old for the current window.
	if seq < rb.baseSeq {
		rb.mu.Unlock()
		return
	}

	// Place segment by sequence number to handle out-of-order arrivals safely.
	idx := int(seq % uint32(rb.capacity))
	rb.buf[idx] = Segment{
		Seq:            seq,
		Data:           data,
		PTS:            pts,
		Time:           rb.now(),
		ActualDuration: actualDuration,
	}
	rb.head = int(rb.nextSeq % uint32(rb.capacity))

	shouldPublish := rb.publishEvents // Read under lock
	rb.mu.Unlock()                    // Unlock before potentially blocking on channel send

	if shouldPublish {
		select {
		case rb.segmentEventChan <- SegmentEvent{Seq: seq}:
		default:
			log.Printf("RingBuffer: SegmentEventChan full, dropping event for seq %d", seq)
		}
	}
}

// GetNextSeq returns the next sequence number that should be used for a new segment.
// This is typically used to determine the -start_number for a new ffmpeg instance.
func (rb *RingBuffer) GetNextSeq() uint32 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.nextSeq
}

// Reset clears the buffer and sets the baseSeq and nextSeq to newBase.
// The head is reset to 0. All segment data is zeroed out.
// This is used when a new file starts streaming to ensure a clean slate for its segments.
func (rb *RingBuffer) Reset(newBase uint32) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.baseSeq, rb.nextSeq, rb.head = newBase, newBase, 0
	// Clear out old segment data to prevent stale data being served if seq numbers wrap unexpectedly (highly unlikely).
	for i := range rb.buf {
		rb.buf[i] = Segment{}
	} // Zero out the Segment structs
}

// GetSegmentActualDuration returns the actual duration for a given sequence, or 0 if not found/set.
func (rb *RingBuffer) GetSegmentActualDuration(seq uint32) float64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if idx := rb.idxFor(seq); idx >= 0 {
		return rb.buf[idx].ActualDuration
	}
	return 0 // Return 0 or a default nominal duration if not found
}
