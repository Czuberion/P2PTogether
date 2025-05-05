package media

import (
	pb "peer_service/proto/client"
	"time"
)

func StartStatusTicker(rb *RingBuffer, every time.Duration) <-chan *pb.StreamStatus {
	out := make(chan *pb.StreamStatus, 8)

	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			<-ticker.C
			base, _ := rb.Snapshot()
			if base == 0 { // encoder not yet live
				continue
			}
			out <- &pb.StreamStatus{
				Sequence: base,
				// FilePath, PTS etc. can be filled once ingest provides them
				IsLive: true,
			}
		}
	}()
	return out
}
