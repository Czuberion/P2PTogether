// peer_service/internal/media/ingest.go
package media

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DurationProvider is an interface that can provide the actual duration for a segment.
type DurationProvider interface {
	GetActualSegmentDuration(globalSeqNum uint32) (float64, bool)
}

// IngestHandler accepts 2‑second MPEG‑TS chunks pushed by FFmpeg.
//
//   - URL pattern:  /ingest/<seq>.ts            (PUT or POST)
//   - Content‑Type: video/MP2T
//   - Body:         complete TS segment (~2 s)
//
// On success we respond 202 Accepted; anything else returns the
// appropriate 4xx/5xx so FFmpeg retries according to its policy.
func IngestHandler(rb *RingBuffer, durationGetter DurationProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Extract sequence number from “…/ingest/123.ts”.
		seqStr := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ingest/"), ".ts")
		seq, err := strconv.ParseUint(seqStr, 10, 32)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		log.Printf("[IngestHandler] Received segment via PUT: seq=%d, path=%s", seq, r.URL.Path)

		// Read the whole segment (≤ ~1 MiB for 2 s 720p).
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "io error: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// var actualDuration float64 = SegmentDuration.Seconds() // Default to nominal
		pts := 0.0 // Assuming PTS for now

		var actualDuration float64
		var durationFound bool

		// Polling loop for duration with timeout
		// This timeout should be fairly short, as ffmpeg should update its M3U8 quickly.
		timeout := time.After(500 * time.Millisecond)         // e.g., 500ms timeout
		pollInterval := time.NewTicker(20 * time.Millisecond) // Poll frequently
		defer pollInterval.Stop()

	getDurationLoop:
		for {
			select {
			case <-timeout:
				log.Printf("[IngestHandler-Sync] Timeout waiting for precise duration for segment %d. Using nominal.", seq)
				actualDuration = SegmentDuration.Seconds() // Nominal duration
				durationFound = false                      // Mark as not precisely found
				break getDurationLoop
			case <-pollInterval.C:
				if durationGetter != nil {
					currentDur, found := durationGetter.GetActualSegmentDuration(uint32(seq))
					if found {
						// log.Printf("[IngestHandler-Sync] Precise duration %.6f found for segment %d.", currentDur, seq)
						actualDuration = currentDur
						durationFound = true
						break getDurationLoop
					}
					// else, log.Printf("[IngestHandler-Sync] Poll: Duration for seg %d not yet available from monitor.", seq)
				} else {
					// This case should ideally not happen if IngestHandler is on the streamer node
					// and the srvInstance.node was correctly passed as durationGetter.
					log.Printf("[IngestHandler-Sync] No duration getter available for segment %d. Using nominal.", seq)
					actualDuration = SegmentDuration.Seconds()
					durationFound = false
					break getDurationLoop
				}
			}
		}

		// honour the incoming segment number
		rb.WriteAt(uint32(seq), data, pts, actualDuration)
		log.Printf("[IngestHandler] Wrote segment %d to RingBuffer with duration %.6f (precise_found: %v)", seq, actualDuration, durationFound)

		w.WriteHeader(http.StatusAccepted)
	}
}
