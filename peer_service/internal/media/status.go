package media

import (
	p2ppb "peer_service/proto/p2p"
	"time"
)

func StartStatusTicker(rb *RingBuffer, every time.Duration) <-chan *p2ppb.StreamStatus {
	out := make(chan *p2ppb.StreamStatus, 8)

	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			<-ticker.C
			base, _ := rb.Snapshot()
			if base == 0 { // encoder not yet live
				continue
			}
			out <- &p2ppb.StreamStatus{
				Sequence: base,
				// FilePath, PTS etc. can be filled once ingest provides them
				IsLive: true,
			}
		}
	}()
	return out
}
