// peer_service/internal/media/ingest.go
package media

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

// IngestHandler accepts 2‑second MPEG‑TS chunks pushed by FFmpeg.
//
//   - URL pattern:  /ingest/<seq>.ts            (PUT or POST)
//   - Content‑Type: video/MP2T
//   - Body:         complete TS segment (~2 s)
//
// On success we respond 202 Accepted; anything else returns the
// appropriate 4xx/5xx so FFmpeg retries according to its policy.
func IngestHandler(rb *RingBuffer) http.HandlerFunc {
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

		// Read the whole segment (≤ ~1 MiB for 2 s 720p).
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "io error: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// NB: we pass PTS = 0 for now.  If/when you need discontinuities
		// or accurate #EXT‑X‑PROGRAM‑DATE‑TIME, expose PTS in the request.
		// rb.Write(data, 0 /*pts*/)

		// honour the incoming segment number
		rb.WriteAt(uint32(seq), data, 0 /*pts*/)

		w.WriteHeader(http.StatusAccepted)
	}
}
