// peer_service/internal/media/ring_buffer_test.go
package media_test

import (
	"bytes"
	"testing"

	"peer_service/internal/media"
)

func TestRingBufferBasic(t *testing.T) {
	rb := media.NewRingBuffer(6) // 3 slots

	fill := func(b byte) []byte { return bytes.Repeat([]byte{b}, 1) }

	for _, ch := range []byte{'A', 'B', 'C', 'D', 'E'} {
		rb.Write(fill(ch), 0)
	}
	base, segs := rb.Snapshot()
	if base != 2 || len(segs) != 3 { // live: seq 2,3,4
		t.Fatalf("after 5 writes base=%d len=%d", base, len(segs))
	}

	rb.Write(fill('F'), 0) // seq 5
	base, segs = rb.Snapshot()
	if base != 3 || segs[0].Seq != 3 || segs[2].Seq != 5 {
		t.Fatalf("after 6 writes unexpected window (base=%d %#v)", base, segs)
	}
}
