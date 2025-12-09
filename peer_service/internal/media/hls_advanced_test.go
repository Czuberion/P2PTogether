// peer_service/internal/media/hls_advanced_test.go
package media_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"peer_service/internal/media"
)

// TestHLS_PlaylistGeneration verifies that the HLS handler correctly generates
// the m3u8 playlist based on the RingBuffer state.
func TestHLS_PlaylistGeneration(t *testing.T) {
	// 1. Setup
	rb := media.NewRingBuffer(20) // 10 segments capacity
	handlerFunc, _, triggerDiscont := media.Handler(rb)

	// Helper to get playlist body
	getPlaylist := func() string {
		req := httptest.NewRequest("GET", "/stream.m3u8", nil)
		w := httptest.NewRecorder()
		handlerFunc(w, req)
		return w.Body.String()
	}

	// 2. Scenario: Empty Buffer
	// Should return a valid header with Media Sequence 0
	body := getPlaylist()
	if !strings.Contains(body, "#EXTM3U") {
		t.Error("Playlist missing #EXTM3U header")
	}
	if !strings.Contains(body, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Error("Expected Media Sequence 0 for empty buffer")
	}

	// 3. Scenario: Buffer with Segments
	// Write 3 segments. Actual duration 2.0
	rb.Write([]byte("s0"), 0, 2.000)
	rb.Write([]byte("s1"), 2, 2.001) // Slight variation
	rb.Write([]byte("s2"), 4, 1.999)

	body = getPlaylist()
	// Check standard segments
	if !strings.Contains(body, "#EXTINF:2.000000,\nseg_0.ts") {
		t.Errorf("Missing or incorrect entry for seg 0. Body:\n%s", body)
	}
	if !strings.Contains(body, "#EXTINF:2.001000,\nseg_1.ts") {
		t.Errorf("Missing or incorrect entry for seg 1")
	}
	if !strings.Contains(body, "#EXTINF:1.999000,\nseg_2.ts") {
		t.Errorf("Missing or incorrect entry for seg 2")
	}

	// 4. Scenario: Discontinuity
	// Trigger discontinuity and check if tag appears
	triggerDiscont()
	body = getPlaylist()
	if !strings.Contains(body, "#EXT-X-DISCONTINUITY") {
		t.Error("Expected #EXT-X-DISCONTINUITY after trigger")
	}

	// Fetch again -> Discontinuity tag should be gone (consumed)
	body = getPlaylist()
	if strings.Contains(body, "#EXT-X-DISCONTINUITY") {
		t.Error("Discontinuity tag persists after being served once")
	}

	// 5. Scenario: Buffer Eviction / Sequence Increase
	// Fill buffer to force eviction. Capacity is 10. We have 3. Write 8 more -> Total 11.
	// Seq 0 should be evicted. Base should start at 1.
	for i := 3; i < 11; i++ {
		rb.Write([]byte("fill"), float64(i*2), 2.0)
	}
	body = getPlaylist()
	if !strings.Contains(body, "#EXT-X-MEDIA-SEQUENCE:1") {
		t.Errorf("Expected Media Sequence 1 after overwrite. Body:\n%s", body)
	}
	if strings.Contains(body, "seg_0.ts") {
		t.Error("Segment 0 should not be in playlist after eviction")
	}
	if !strings.Contains(body, "seg_1.ts") {
		t.Error("Segment 1 should be present as the first segment")
	}
}
