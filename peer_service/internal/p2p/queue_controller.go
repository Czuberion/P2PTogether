package p2p

import (
	"context"
	"fmt"
	"log"

	"peer_service/internal/common"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

type QueueController struct {
	q                    *Queue
	triggerDiscontinuity func() // Function to call for HLS discontinuity
}

func NewQueueController(triggerDisc func()) *QueueController {
	return &QueueController{
		q:                    NewQueue(),
		triggerDiscontinuity: triggerDisc,
	}
}

// Q returns the underlying queue. Used for read-only operations like checking head.
func (qc *QueueController) Q() *Queue {
	return qc.q
}

func (qc *QueueController) Snapshot() *clientpb.ServerMsg {
	it := qc.q.Items()
	pbItems := make([]*p2ppb.QueueItem, 0, len(it))
	for _, x := range it {
		pbItems = append(pbItems, &p2ppb.QueueItem{
			FilePath: x.FilePath,
			StoredBy: x.StoredBy.String(),
			AddedBy:  x.AddedBy.String(),
			HlcTs:    x.HlcTs,
		})
	}
	return &clientpb.ServerMsg{
		Payload: &clientpb.ServerMsg_QueueUpdate{QueueUpdate: &p2ppb.QueueUpdate{Items: pbItems}},
	}
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
	currentHeadBeforeChange, _ := qc.q.HeadNoLock()

	switch cmd.Type {
	case p2ppb.QueueCmd_APPEND:
		qc.q.Append(QueueItem{
			FilePath: cmd.FilePath,
			StoredBy: sender,
			AddedBy:  sender,
			HlcTs:    cmd.HlcTs,
		})
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
		if qc.triggerDiscontinuity != nil {
			log.Printf("QueueController: Triggering HLS discontinuity (with RingBuffer reset to Node's next expected seq) for NEXT command.")
			qc.triggerDiscontinuity()
		}
		if qc.q.PopHead() { // PopHead returns true if an item was popped
			madeChange = true
		} else {
			log.Println("QueueController: NEXT called on empty or single-item queue, no change after pop.")
			// If queue becomes empty, ensure encoder stops
			node.StopCurrentEncoder()
		}
	case p2ppb.QueueCmd_SKIP_NEXT: // User-initiated skip
		// Permission already checked if not internal.
		if qc.triggerDiscontinuity != nil {
			log.Printf("QueueController: Triggering HLS discontinuity (with RingBuffer reset to Node's next expected seq) for SKIP_NEXT command.")
			qc.triggerDiscontinuity()
		}
		// Stop the current encoder *before* popping the head,
		// so ReactToQueueUpdate doesn't try to restart it for the item being skipped.
		node.StopCurrentEncoder()

		if qc.q.PopHead() {
			madeChange = true
		} else {
			log.Println("QueueController: SKIP_NEXT called on empty or single-item queue, no change after pop.")
		}
	default:
		return fmt.Errorf("unknown QueueCmd type: %v", cmd.Type)
	}

	if madeChange {
		newHeadAfterChange, newHeadOk := qc.q.HeadNoLock()
		logMsg := fmt.Sprintf("QueueController: Queue changed due to %s cmd from %s.", cmd.Type, sender)
		if newHeadOk {
			logMsg += fmt.Sprintf(" Old head: %s, New head: %s.", currentHeadBeforeChange.FilePath, newHeadAfterChange.FilePath)
		}
		log.Println(logMsg)
		snapshotMsg := qc.Snapshot() // Get the *clientpb.ServerMsg
		// Add HLC to the QueueUpdate payload itself
		if quPayload, ok := snapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
			quPayload.QueueUpdate.HlcTs = common.GetCurrentHLC() // Set HLC for the update event
		}

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
			HlcTs:    pbItem.HlcTs,
		})
	}
	qc.q.ReplaceItems(newItems)

	if localHub != nil {
		snapshotMsg := qc.Snapshot()
		// Update the HLC on the snapshot payload to reflect this event's time
		if quPayload, ok := snapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
			quPayload.QueueUpdate.HlcTs = common.GetCurrentHLC()
		}
		localHub.Broadcast(snapshotMsg)
	}

	node.ReactToQueueUpdate()
}
