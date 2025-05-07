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

func NewQueue() *Queue {
	return &Queue{items: make([]QueueItem, 0)}
}

// ReplaceItems replaces all items in the queue with the new set.
func (q *Queue) ReplaceItems(newItems []QueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = make([]QueueItem, len(newItems)) // Create a new slice
	copy(q.items, newItems)
}

// Helper methods for controlled access during complex operations (e.g., failover)

func (q *Queue) HeadNoLock() (QueueItem, bool) {
	if len(q.items) == 0 {
		return QueueItem{}, false
	}
	return q.items[0], true
}
func (q *Queue) PopHeadNoLock() {
	if len(q.items) > 0 {
		q.items = append(q.items[:0], q.items[1:]...)
	}
}
func (q *Queue) ItemsNoLock() []QueueItem {
	out := make([]QueueItem, len(q.items))
	copy(out, q.items)
	return out
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

func (q *Queue) Items() []QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]QueueItem, len(q.items))
	copy(out, q.items) // return a copy so callers can’t mutate us
	return out
}
