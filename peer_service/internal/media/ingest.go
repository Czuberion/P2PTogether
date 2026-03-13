// peer_service/internal/media/ingest.go
package media

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
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

		// Keep ingest hot-path non-blocking. Waiting for M3U8 monitor data here can
		// reorder segment writes under concurrent PUTs and delay nextSeq updates.
		actualDuration := SegmentDuration.Seconds()
		durationFound := false
		if durationGetter != nil {
			if currentDur, found := durationGetter.GetActualSegmentDuration(uint32(seq)); found && currentDur > 0 {
				actualDuration = currentDur
				durationFound = true
			}
		}

		// honour the incoming segment number
		rb.WriteAt(uint32(seq), data, pts, actualDuration)
		log.Printf("[IngestHandler] Wrote segment %d to RingBuffer with duration %.6f (precise_found: %v)", seq, actualDuration, durationFound)

		w.WriteHeader(http.StatusAccepted)
	}
}
