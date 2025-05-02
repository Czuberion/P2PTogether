// internal/media/hls.go
package media

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
)

// Handler returns two ready‑to‑use mux handlers:
//   - /stream.m3u8  – rolling playlist
//   - /seg_123.ts   – raw MPEG‑TS segment
func Handler(rb *RingBuffer) (playlist http.HandlerFunc, segment http.HandlerFunc) {

	playlist = func(w http.ResponseWriter, r *http.Request) {
		base, segs := rb.Snapshot()
		if len(segs) == 0 {
			http.Error(w, "#EXTM3U\n# empty", http.StatusServiceUnavailable)
			return
		}

		var buf bytes.Buffer
		buf.WriteString("#EXTM3U\n")
		buf.WriteString("#EXT-X-VERSION:3\n")
		buf.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
		buf.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(SegmentDuration.Seconds())))
		buf.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", base))

		for _, s := range segs {
			buf.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", SegmentDuration.Seconds()))
			buf.WriteString(fmt.Sprintf("seg_%d.ts\n", s.Seq))
		}

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
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
