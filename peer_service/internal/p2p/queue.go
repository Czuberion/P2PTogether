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
	HlcTs    int64   // hybrid logical clock of when item was added/modified
	// SeqBase  uint32  // Optional: Starting sequence number for this item if it streams
}

// Queue is thread-safe and order-preserving.
type Queue struct {
	items []QueueItem
	mu    sync.RWMutex
}

func NewQueue() *Queue {
	return &Queue{items: make([]QueueItem, 0)}
}

// ReplaceItems replaces all items in the queue with the new set.
// This method is used by ApplyUpdate when a full state is received.
func (q *Queue) ReplaceItems(newItems []QueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = make([]QueueItem, len(newItems)) // Create a new slice
	copy(q.items, newItems)
}

// Helper methods for controlled access during complex operations (e.g., failover)

func (q *Queue) HeadNoLock() (QueueItem, bool) {
	// Assumes q.mu is already RLock'ed or Lock'ed by caller
	if len(q.items) == 0 {
		return QueueItem{}, false
	}
	return q.items[0], true
}

func (q *Queue) PopHeadNoLock() bool {
	// Assumes q.mu is already Lock'ed by caller
	if len(q.items) > 0 {
		q.items = q.items[1:]
		return true
	}
	return false
}

func (q *Queue) ItemsNoLock() []QueueItem {
	// Assumes q.mu is already RLock'ed or Lock'ed by caller
	out := make([]QueueItem, len(q.items))
	copy(out, q.items)
	return out
}

func (q *Queue) Append(it QueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, it)
}

// Remove removes an item at a given index. Returns true if an item was removed.
func (q *Queue) Remove(idx int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if idx >= 0 && idx < len(q.items) {
		q.items = append(q.items[:idx], q.items[idx+1:]...)
		return true
	}
	return false
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
	// Return a copy to prevent external modification of the item in the slice
	itemCopy := q.items[0]
	return itemCopy, true
}

// PopHead removes the first item from the queue. Returns true if an item was popped.
func (q *Queue) PopHead() bool {
	q.mu.Lock() // Needs full lock for modification
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return false
	}
	q.items = q.items[1:]
	return true
}

func (q *Queue) Items() []QueueItem {
	q.mu.RLock()
	defer q.mu.RUnlock()

	out := make([]QueueItem, len(q.items))
	copy(out, q.items) // return a copy so callers can’t mutate us
	return out
}

func (q *Queue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items)
}
