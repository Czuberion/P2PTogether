package p2p

import (
	"context"
	"fmt"
	"log"
	"sync"

	"peer_service/internal/common"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

// QueueController manages the media queue, its state transitions, and interactions with the Node.
type QueueController struct {
	q                    *Queue
	triggerDiscontinuity func() // Function to call for HLS discontinuity

	// Global media sequence management
	mu                   sync.Mutex // Protects currentMediaSequence
	currentMediaSequence uint32     // The next sequence number to be assigned to a new stream segment from a new ffmpeg instance.
}

// NewQueueController creates a new QueueController.
func NewQueueController(triggerDisc func()) *QueueController {
	return &QueueController{
		q:                    NewQueue(),
		triggerDiscontinuity: triggerDisc,
		// currentMediaSequence will start at 0 by default, which is correct
		// for the very first segment of the entire session.
	}
}

// Q returns the underlying queue. Used for read-only operations like checking head.
func (qc *QueueController) Q() *Queue {
	return qc.q
}

// GetAndAdvanceNextStartSequence provides the sequence number that a new ffmpeg instance
// (for a new queue item or a seek-restart) should use for its -start_number.
// It also advances the internal counter by the number of segments of the *just finished* stream.
func (qc *QueueController) GetNextStartSequence() uint32 {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	// For now, simply return the current. The responsibility to update it after a stream
	// is complex and depends on knowing how many segments the *previous* stream had.
	// This will be refined. For starting a *new* distinct stream, this sequence is used.
	return qc.currentMediaSequence
}

// UpdateGlobalMediaSequence sets the global media sequence. This should be called
// by the Node that just finished streaming its item, reporting its RingBuffer's nextSeq.
func (qc *QueueController) UpdateGlobalMediaSequence(lastStreamNextSeq uint32) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	if lastStreamNextSeq > qc.currentMediaSequence {
		log.Printf("QueueController: Updating global media sequence from %d to %d", qc.currentMediaSequence, lastStreamNextSeq)
		qc.currentMediaSequence = lastStreamNextSeq
	}
}

func (qc *QueueController) Snapshot() *clientpb.ServerMsg {
	it := qc.q.Items()
	pbItems := make([]*p2ppb.QueueItem, 0, len(it))
	for _, x := range it {
		pbItems = append(pbItems, &p2ppb.QueueItem{
			FilePath: x.FilePath,
			StoredBy: x.StoredBy.String(),
			AddedBy:  x.AddedBy.String(),
			FirstSeq: x.FirstSeq,
			NumSegs:  x.NumSegs,
			HlcTs:    x.HlcTs,
		})
	}
	// HlcTs for QueueUpdate itself will be set by caller
	return &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_QueueUpdate{
		QueueUpdate: &p2ppb.QueueUpdate{Items: pbItems, HlcTs: common.GetCurrentHLC()}}}
}

// Handle processes a queue command, mutates the queue, and notifies local/remote clients.
// It is called when a local gRPC client sends a QueueCmd.
// It uses the RoleManager to check permissions.
func (qc *QueueController) Handle(ctx context.Context, cmd *p2ppb.QueueCmd, sender peer.ID, rm *roles.RoleManager, hub *Hub, node *Node, ctrlTopic *pubsub.Topic) error {
	// Permission check for user-initiated commands
	isInternalNext := cmd.Type == p2ppb.QueueCmd_NEXT && sender == node.ID() // Check if it's an internal NEXT
	if !isInternalNext {
		perms := rm.GetPermissionsForPeer(sender)
		if !perms.Has(roles.PermQueue) {
			return fmt.Errorf("permission denied: peer %s missing Queue perm for cmd type %s", sender, cmd.Type)
		}
	}

	madeChange := false
	currentHeadBeforeChange, headExistedBeforeChange := qc.q.Head()
	cursorBeforeChange := qc.q.GetCursor()

	switch cmd.Type {
	case p2ppb.QueueCmd_APPEND:
		// qc.q.Append(QueueItem{

		// ffprobe should be run here to get NumSegs. For now, placeholder.
		// Assume NumSegs comes from client or is calculated (e.g. ffprobe call before sending APPEND).
		// For LAN-MVP, we might not have NumSegs accurately until ffmpeg runs.
		// Let's set a placeholder for now. Client doesn't send it yet.
		numSegsPlaceholder := uint32(0) // TODO: Populate this accurately
		if cmd.FilePath != "" {         // Basic validation
			qc.q.Append(QueueItem{
				FilePath: cmd.FilePath,
				StoredBy: sender,
				AddedBy:  sender,
				NumSegs:  numSegsPlaceholder,
				HlcTs:    cmd.HlcTs,
			})
		}
		madeChange = true
	case p2ppb.QueueCmd_REMOVE:
		if qc.q.Remove(int(cmd.Index)) {
			madeChange = true
		}
	case p2ppb.QueueCmd_CLEAR:
		qc.q.Clear()
		madeChange = true
		// After clearing, the current streamer (if any) should stop.
		node.StopCurrentEncoder() // Explicitly stop encoder.
	case p2ppb.QueueCmd_NEXT: // Typically server-initiated after EOF
		// if qc.triggerDiscontinuity != nil {
		// 	// This discontinuity is for the *next* stream if one starts.
		// 	// The new streamer's hls.go will use this.
		// 	log.Printf("QueueController: Marking for HLS discontinuity for the *next* stream due to NEXT command.")
		// 	qc.triggerDiscontinuity()
		// }
		// if qc.q.PopHead() { // PopHead returns true if an item was popped
		log.Printf("QueueController: Processing NEXT command from %s. Current cursor: %d", sender, qc.q.GetCursor())

		// Update global media sequence with the end of the *just finished* stream.
		// The node that sent NEXT (EOF) should have its ringbuffer's nextSeq reflect this.
		// We assume the sender (node) will provide this.
		// For now, let's assume the ring buffer for 'node' is the one that just finished.
		// This logic needs refinement if sender of NEXT isn't always the finishing streamer.
		if node != nil && node.Host.ID() == sender && node.RingBuffer() != nil {
			qc.UpdateGlobalMediaSequence(node.RingBuffer().GetNextSeq())
		}

		newHeadItem, advanced := qc.q.AdvanceCursor()
		if advanced {
			madeChange = true
			// log.Printf("QueueController: Popped head for NEXT. Queue length now %d.", qc.q.Len())
			// // If a new item becomes head, ReactToQueueUpdate (called later) will handle starting it.
			// // The new item will use qc.GetNextStartSequence().
			// newHeadAfterPop, newHeadOkAfterPop := qc.q.Head()
			// if newHeadOkAfterPop {
			// 	// Broadcast PlaylistReset for the new upcoming stream
			// 	// The sequence here should be the one the *new* stream will start with.
			// 	nextStartSeq := qc.GetNextStartSequence()
			// 	playlistResetMsg := &p2ppb.PlaylistReset{Sequence: nextStartSeq, StartPts: 0.0, HlcTs: common.GetCurrentHLC()}
			// 	serverPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaylistReset{PlaylistReset: playlistResetMsg}}
			// 	log.Printf("QueueController: Broadcasting PlaylistReset for new head %s, nextStartSeq: %d", newHeadAfterPop.FilePath, nextStartSeq)
			// 	if hub != nil {
			// 		hub.Broadcast(serverPayload)
			// 	}
			// 	if ctrlTopic != nil {
			// 		// Publish PlaylistReset to control topic
			// 		if marshalledMsg, err := proto.Marshal(serverPayload); err == nil {
			// 			ctrlTopic.Publish(ctx, marshalledMsg)
			log.Printf("QueueController: Advanced cursor for NEXT. New cursor: %d, Item: %s", qc.q.GetCursor(), newHeadItem.FilePath)

			// If a new item becomes current, the *new streamer* (determined by ReactToQueueUpdate)
			// will be responsible for resetting its RingBuffer and emitting PlaylistReset.
			// This Handle function should not emit PlaylistReset directly for NEXT cmd.
			// ReactToQueueUpdate (called at the end) will trigger the new streamer.

			// The triggerDiscontinuity is for the HLS server that WILL serve the new stream.
			// It should be called by the Node *when it starts streaming a new item*.
			// So, remove direct call here. Node.EnsureRunnerState will handle it.

		} else {
			log.Println("QueueController: NEXT called, but cursor did not advance (reached end or empty). No change.")
			// If queue becomes empty or cursor is invalid, ensure encoder stops
			node.StopCurrentEncoder()
		}

	case p2ppb.QueueCmd_SKIP_NEXT: // User-initiated skip
		// Permission already checked.
		// Stop the current encoder *before* advancing the cursor.
		node.StopCurrentEncoder()

		// Similar to NEXT, advance cursor.
		// The new streamer (if any) will handle PlaylistReset and discontinuity.
		log.Printf("QueueController: Processing SKIP_NEXT command from %s. Current cursor: %d", sender, qc.q.GetCursor())
		if node != nil && node.Host.ID() == sender && node.RingBuffer() != nil {
			qc.UpdateGlobalMediaSequence(node.RingBuffer().GetNextSeq())
		}
		newHeadItemAfterSkip, advancedSkip := qc.q.AdvanceCursor()
		if advancedSkip {
			madeChange = true
			log.Printf("QueueController: Advanced cursor for SKIP_NEXT. New cursor: %d, Item: %s", qc.q.GetCursor(), newHeadItemAfterSkip.FilePath)
		} else {
			log.Println("QueueController: SKIP_NEXT called, but cursor did not advance. No change.")
		}
		// 			}
		// 		}
		// 	}
		// } else {
		// 	log.Println("QueueController: NEXT called on empty or single-item queue, no change after pop.")
		// 	// If queue becomes empty, ensure encoder stops
		// 	node.StopCurrentEncoder()
		// }
	default:
		return fmt.Errorf("unknown QueueCmd type: %v", cmd.Type)
	}

	if madeChange {
		newHeadAfterChange, newHeadOk := qc.q.Head()
		logMsg := fmt.Sprintf("QueueController: Queue changed due to %s cmd from %s.", cmd.Type, sender)
		if headExistedBeforeChange {
			logMsg += fmt.Sprintf(" Old head: %s.", currentHeadBeforeChange.FilePath)
		}
		if newHeadOk {
			logMsg += fmt.Sprintf(" New head: %s.", newHeadAfterChange.FilePath)
		}
		logMsg += fmt.Sprintf(" Old cursor: %d, New cursor: %d.", cursorBeforeChange, qc.q.GetCursor())
		log.Println(logMsg)
		snapshotMsg := qc.Snapshot() // Get the *clientpb.ServerMsg
		// // Add HLC to the QueueUpdate payload itself
		// if quPayload, ok := snapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
		// 	quPayload.QueueUpdate.HlcTs = common.GetCurrentHLC() // Set HLC for the update event
		// }

		if hub != nil {
			hub.Broadcast(snapshotMsg)
		}
		if ctrlTopic != nil {
			if marshalledServerMsg, err := proto.Marshal(snapshotMsg); err != nil {
				log.Printf("[gossip QC.Handle] marshal ServerMsg{QueueUpdate} failed: %v", err)
			} else if err := ctrlTopic.Publish(ctx, marshalledServerMsg); err != nil {
				if ctx.Err() != nil {
					log.Printf("[gossip QC.Handle] context error during publish ServerMsg{QueueUpdate}: %v", ctx.Err())
				} else {
					log.Printf("[gossip QC.Handle] publish ServerMsg{QueueUpdate} failed: %v", err)
				}
			}
		}
	}

	// Always call ReactToQueueUpdate, even if no change from this specific command,
	// as other state (like permissions) might have changed, or an empty queue still needs handling.
	node.ReactToQueueUpdate()
	return nil
}

// ApplyUpdate replaces the entire local queue state with the items from the received update.
// This is called when a QueueUpdate is received from GossipSub.
// node and rm are passed to facilitate calling ReactToQueueUpdate.
func (qc *QueueController) ApplyUpdate(update *p2ppb.QueueUpdate, localHub *Hub, node *Node, rm *roles.RoleManager) {
	// TODO: Implement HLC check for the update message itself (update.HlcTs)
	//       against a locally stored HLC for the last applied queue update.
	//       For now, always apply.
	log.Printf("QueueController.ApplyUpdate: Applying full queue update with HLC %d. Items: %d", update.HlcTs, len(update.Items))

	newItems := make([]QueueItem, 0, len(update.Items))
	for _, pbItem := range update.Items {
		storedByID, errStored := peer.Decode(pbItem.StoredBy)
		if errStored != nil {
			log.Printf("ApplyUpdate: invalid StoredBy peer ID '%s': %v", pbItem.StoredBy, errStored)
			continue
		}
		addedByID, errAdded := peer.Decode(pbItem.AddedBy)
		if errAdded != nil {
			log.Printf("ApplyUpdate: invalid AddedBy peer ID '%s': %v", pbItem.AddedBy, errAdded)
			continue
		}
		newItems = append(newItems, QueueItem{
			FilePath: pbItem.FilePath,
			StoredBy: storedByID,
			AddedBy:  addedByID,
			FirstSeq: pbItem.FirstSeq,
			NumSegs:  pbItem.NumSegs,
			HlcTs:    pbItem.HlcTs,
		})
	}
	qc.q.ReplaceItems(newItems)

	// if localHub != nil {
	// 	snapshotMsg := qc.Snapshot()
	// 	// Update the HLC on the snapshot payload to reflect this event's time
	// 	if quPayload, ok := snapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
	// 		quPayload.QueueUpdate.HlcTs = common.GetCurrentHLC()
	// 	}
	// 	localHub.Broadcast(snapshotMsg)
	// }
	// qc.q.SetCursor(...) // TODO: If the update also implies a cursor change from gossip, handle it.
	// For now, assume gossip only syncs item list, local events (NEXT) change cursor.

	// Broadcast the newly applied state to local clients.
	// Snapshot already sets HLC.
	if localHub != nil {
		localHub.Broadcast(qc.Snapshot())
	}

	node.ReactToQueueUpdate()
}
