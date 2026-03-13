// internal/media/hls.go
package media

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

// Handler returns two ready‑to‑use mux handlers:
//   - /stream.m3u8  – rolling playlist
//   - /seg_XXX.ts   – raw MPEG‑TS segment
//
// It also returns a function to trigger a discontinuity.
func Handler(rb *RingBuffer) (playlist http.HandlerFunc, segment http.HandlerFunc, triggerDiscontinuity func()) {
	// pendingDiscont is set true by triggerDiscontinuity and reset after emitting
	var pendingDiscont bool

	// triggerDiscontinuity tells the playlist to emit an EXT-X-DISCONTINUITY tag
	triggerDiscontinuity = func() {
		log.Println("[HLS Handler] Discontinuity triggered.")
		pendingDiscont = true
	}

	// build the playlist handler
	playlist = func(w http.ResponseWriter, r *http.Request) {
		base, segs := rb.Snapshot()
		// Always return **200 OK** so mpv can keep polling while the buffer
		// is still empty. A syntactically‑valid stub avoids the 503 → fatal
		// error.
		var buf bytes.Buffer

		buf.WriteString("#EXTM3U\n")
		buf.WriteString("#EXT-X-VERSION:3\n")
		buf.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
		// Hint clients to begin from playlist start, not from live edge.
		buf.WriteString("#EXT-X-START:TIME-OFFSET=0,PRECISE=YES\n")
		buf.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(SegmentDuration.Seconds())))
		buf.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", base))

		// insert discontinuity marker if we just hand-off to a new streamer
		if pendingDiscont {
			buf.WriteString("#EXT-X-DISCONTINUITY\n")
			pendingDiscont = false
		}

		// If we already have segments, list them; otherwise return just the header.
		for _, s := range segs {
			durationToUse := SegmentDuration.Seconds() // Fallback to nominal
			if s.ActualDuration > 0 {
				durationToUse = s.ActualDuration
			}
			// HLS spec recommends 6 decimal places for EXTINF
			buf.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", durationToUse))
			buf.WriteString(fmt.Sprintf("seg_%d.ts\n", s.Seq))
		}

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(http.StatusOK)
		w.Write(buf.Bytes())
	}

	segment = func(w http.ResponseWriter, r *http.Request) {
		seqStr := r.URL.Path[len("/seg_") : len(r.URL.Path)-len(".ts")]
		seq, err := strconv.ParseUint(seqStr, 10, 32)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data := rb.Get(uint32(seq))
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/MP2T")
		w.Write(data)
	}

	return
}
