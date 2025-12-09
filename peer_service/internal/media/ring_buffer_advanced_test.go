// peer_service/internal/media/ring_buffer_advanced_test.go
package media_test

import (
	"fmt"
	"testing"

	"peer_service/internal/media"
)

// TestRingBuffer_CircularOverwrite verifies that the buffer correctly evicts
// the oldest segments when capacity is exceeded.
func TestRingBuffer_CircularOverwrite(t *testing.T) {
	// 1. Setup: Create a buffer with capacity for 60 seconds (30 segments of 2s each)
	// Usage: NewRingBuffer(windowSeconds) -> capacity = windowSeconds / 2
	windowSeconds := 60
	capacity := windowSeconds / int(media.SegmentDuration.Seconds()) // 30
	rb := media.NewRingBuffer(windowSeconds)

	// 2. Action: Write 'capacity + 1' segments
	// We write 31 segments. Segment 0 should be evicted. Segment 1 should be the new base.
	totalSegments := capacity + 1
	for i := 0; i < totalSegments; i++ {
		payload := []byte(fmt.Sprintf("seg-%d", i))
		rb.Write(payload, float64(i)*2.0, 2.0)
	}

	// 3. Assertion: Verify Snapshot state
	baseSeq, segs := rb.Snapshot()

	// Base sequence should be 1 (since 0 was evicted)
	expectedBase := uint32(1)
	if baseSeq != expectedBase {
		t.Errorf("Expected baseSeq to be %d, got %d", expectedBase, baseSeq)
	}

	// Length should be equal to capacity (30)
	if len(segs) != capacity {
		t.Errorf("Expected %d segments in snapshot, got %d", capacity, len(segs))
	}

	// Double check the first segment in the snapshot constitutes the new base
	if len(segs) > 0 {
		if segs[0].Seq != expectedBase {
			t.Errorf("Expected first segment seq to be %d, got %d", expectedBase, segs[0].Seq)
		}
		expectedPayload := []byte(fmt.Sprintf("seg-%d", expectedBase))
		if string(segs[0].Data) != string(expectedPayload) {
			t.Errorf("Expected payload %s, got %s", expectedPayload, segs[0].Data)
		}
	}

	// Verify accessing the evicted segment returns nil
	if val := rb.Get(0); val != nil {
		t.Errorf("Expected segment 0 to be evicted (nil), but got data")
	}

	// Verify accessing the last written segment works
	lastSeq := uint32(totalSegments - 1)
	if val := rb.Get(lastSeq); val == nil {
		t.Errorf("Expected segment %d to exist, got nil", lastSeq)
	}
}

// TestRingBuffer_GetNextSeq verifies that the sequence counter behaves correctly
// specifically for seamless stream concatenation logic.
func TestRingBuffer_GetNextSeq(t *testing.T) {
	rb := media.NewRingBuffer(10) // 5 segments capacity

	// Initial state
	if seq := rb.GetNextSeq(); seq != 0 {
		t.Errorf("Expected initial NextSeq to be 0, got %d", seq)
	}

	// Write 3 segments
	rb.Write([]byte("A"), 0, 2.0) // seq 0
	rb.Write([]byte("B"), 2, 2.0) // seq 1
	rb.Write([]byte("C"), 4, 2.0) // seq 2

	// NextSeq should be 3
	if seq := rb.GetNextSeq(); seq != 3 {
		t.Errorf("Expected NextSeq to be 3 after 3 writes, got %d", seq)
	}

	// Reset to a specific sequence (simulation of stream concatenation or skip)
	newBase := uint32(100)
	rb.Reset(newBase)

	// NextSeq should now be 100
	if seq := rb.GetNextSeq(); seq != 100 {
		t.Errorf("Expected NextSeq to be %d after Reset, got %d", newBase, seq)
	}

	// Write one more
	assignedSeq := rb.Write([]byte("X"), 100.0, 2.0)
	if assignedSeq != 100 {
		t.Errorf("Expected Write to return seq 100, got %d", assignedSeq)
	}

	// NextSeq should be 101
	if seq := rb.GetNextSeq(); seq != 101 {
		t.Errorf("Expected NextSeq to be 101, got %d", seq)
	}
}
