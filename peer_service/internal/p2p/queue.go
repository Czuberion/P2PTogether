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
	FirstSeq uint32  // Media sequence of the file's first 2s chunk (set when it becomes current)
	NumSegs  uint32  // Total 2s segments for this file (e.g., from ffprobe)
	HlcTs    int64   // hybrid logical clock of when item was added/modified
}

// Queue is thread-safe and order-preserving.
type Queue struct {
	items  []QueueItem
	mu     sync.RWMutex
	cursor int // index of "current" item (-1 => nothing playing)
}

func NewQueue() *Queue {
	return &Queue{items: make([]QueueItem, 0), cursor: -1}
}

// ReplaceItems replaces all items in the queue with the new set.
// This method is used by ApplyUpdate when a full state is received.
func (q *Queue) ReplaceItems(newItems []QueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = make([]QueueItem, len(newItems)) // Create a new slice
	copy(q.items, newItems)
	// Reset cursor if it's now out of bounds or if the queue is empty.
	q.updateCursorAfterMutation(0) // Check from start
}

// Helper methods for controlled access during complex operations (e.g., failover)

func (q *Queue) HeadNoLock() (QueueItem, bool) {
	// Assumes q.mu is already RLock'ed or Lock'ed by caller
	if len(q.items) == 0 {
		return QueueItem{}, false
	}
	return q.items[q.cursor], q.cursor >= 0 && q.cursor < len(q.items)
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

func (q *Queue) updateCursorAfterMutation(changedAtIndex int) {
	// Assumes q.mu is Lock'ed
	if len(q.items) == 0 {
		q.cursor = -1
		return
	}
	// If the cursor was pointing to an item at or after the change,
	// and that change was a deletion, it might need adjustment.
	// For simplicity here, if an item before or at the cursor is removed,
	// the cursor needs to decrement.
	// If the cursor was beyond the new length, cap it or set to -1.

	if q.cursor >= len(q.items) {
		q.cursor = len(q.items) - 1 // Point to last item if any
	}
	// If cursor was -1 and items were added, it should point to 0
	if q.cursor == -1 && len(q.items) > 0 {
		q.cursor = 0
	}
}

// Append adds an item to the end of the queue.
// If the queue was empty, the cursor is set to point to this new item.
func (q *Queue) Append(it QueueItem) QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, it)

	// If the cursor was -1 (idle/end of queue) and items now exist,
	// set the cursor to the newly added item to make it active.
	if q.cursor == -1 && len(q.items) > 0 {
		q.cursor = len(q.items) - 1
	}
	return it
}

// Remove removes an item at a given index. Returns true if an item was removed.
func (q *Queue) Remove(idx int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if idx < 0 || idx >= len(q.items) {
		return false
	}
	q.items = append(q.items[:idx], q.items[idx+1:]...)

	if len(q.items) == 0 {
		q.cursor = -1
	} else if q.cursor == idx {
		// If the removed item was the current one, advance cursor or set to -1 if it was last
		if idx >= len(q.items) { // If it was the last item
			q.cursor = len(q.items) - 1 // Point to new last, or -1 if empty (handled above)
		}
		// No change to cursor index if it's still valid, items shifted.
		// If cursor was > idx, it needs to be decremented.
	} else if q.cursor > idx {
		q.cursor--
	}
	// If cursor < idx, it's unaffected.
	return true
}

func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
	q.cursor = -1
}

func (q *Queue) Head() (QueueItem, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.cursor < 0 || q.cursor >= len(q.items) {
		return QueueItem{}, false
	}
	// Return a copy to prevent external modification of the item in the slice
	itemCopy := q.items[q.cursor]
	return itemCopy, true
}

// AdvanceCursor moves the cursor to the next item.
// Returns true if the cursor advanced to a valid item, false if it reached the end (or queue is empty).
func (q *Queue) AdvanceCursor() (QueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cursor < 0 || q.cursor+1 >= len(q.items) {
		q.cursor = -1 // Reached end or was already invalid
		return QueueItem{}, false
	}
	q.cursor++
	return q.items[q.cursor], true
}

func (q *Queue) Items() []QueueItem {
	q.mu.RLock()
	defer q.mu.RUnlock()

	out := make([]QueueItem, len(q.items))
	copy(out, q.items) // return a copy so callers can’t mutate us
	return out
}

// SetItemAtCursor updates the item at the current cursor.
// This is used to store first_seq and num_segs when an item starts playing.
func (q *Queue) SetItemAtCursor(item QueueItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cursor >= 0 && q.cursor < len(q.items) {
		q.items[q.cursor] = item
		return true
	}
	return false
}

// GetCursor returns the current cursor index.
func (q *Queue) GetCursor() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.cursor
}

// SetCursor directly sets the cursor. Used carefully, e.g., after a REMOVE or CLEAR, or seek to item.
func (q *Queue) SetCursor(idx int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if idx >= -1 && idx < len(q.items) {
		q.cursor = idx
	}
}

func (q *Queue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items)
}
