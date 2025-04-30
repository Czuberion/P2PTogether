package p2p

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// QueueItem is a single entry in the session playlist.
type QueueItem struct {
	FilePath string  // absolute path on StoredBy peer
	StoredBy peer.ID // peer that has the file locally
	AddedBy  peer.ID // who queued it
	HlcTs    int64   // hybrid logical clock
}

// Queue is thread-safe and order-preserving.
type Queue struct {
	items []QueueItem
	mu    sync.Mutex
}

func (q *Queue) Append(it QueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, it)
}

func (q *Queue) Remove(idx int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if idx >= 0 && idx < len(q.items) {
		q.items = append(q.items[:idx], q.items[idx+1:]...)
	}
}

func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
}

func (q *Queue) Head() (QueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return QueueItem{}, false
	}
	return q.items[0], true
}

func (q *Queue) PopHead() {
	q.Remove(0)
}
