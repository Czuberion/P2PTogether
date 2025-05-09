package p2p

import (
	"context"
	"fmt"
	"log"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

type QueueController struct {
	q *Queue
}

func NewQueueController() *QueueController { return &QueueController{q: NewQueue()} }

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
// The `ctrlTopic` is for publishing the update via GossipSub.
func (qc *QueueController) Handle(ctx context.Context, cmd *p2ppb.QueueCmd, sender peer.ID, rm *roles.RoleManager, hub *Hub, node *Node, ctrlTopic *pubsub.Topic) error {
	perms := rm.GetPermissionsForPeer(sender) // Get permissions for the sender
	if !perms.Has(roles.PermQueue) {
		return fmt.Errorf("permission denied: missing Queue perm")
	}

	switch cmd.Type {
	case p2ppb.QueueCmd_APPEND:
		qc.q.Append(QueueItem{
			FilePath: cmd.FilePath,
			StoredBy: sender,
			AddedBy:  sender,
			HlcTs:    cmd.HlcTs,
		})
	case p2ppb.QueueCmd_REMOVE:
		qc.q.Remove(int(cmd.Index))
	case p2ppb.QueueCmd_CLEAR:
		qc.q.Clear()
	case p2ppb.QueueCmd_NEXT:
		qc.q.PopHead()
	}

	// After any successful mutation:
	// 1. Notify local GUI client(s) via Hub
	snapshotMsg := qc.Snapshot()
	if hub != nil {
		hub.Broadcast(snapshotMsg)
	}

	// 2. Publish the QueueUpdate over GossipSub
	if ctrlTopic != nil {
		if marshalledServerMsg, err := proto.Marshal(snapshotMsg); err != nil {
			log.Printf("[gossip] Handle: marshal ServerMsg{QueueUpdate} failed: %v", err)
		} else if err := ctrlTopic.Publish(ctx, marshalledServerMsg); err != nil {
			// Check context error for Publish
			if ctx.Err() != nil {
				log.Printf("[gossip] Handle: context error during publish ServerMsg{QueueUpdate}: %v", ctx.Err())
			} else {
				log.Printf("[gossip] Handle: publish ServerMsg{QueueUpdate} failed: %v", err)
			}
		}
	}

	// 3. Trigger queue-to-runner glue logic
	node.ReactToQueueUpdate(qc, rm)
	return nil
}

// ApplyUpdate replaces the entire local queue state with the items from the received update.
// This is called when a QueueUpdate is received from GossipSub.
// node and rm are passed to facilitate calling ReactToQueueUpdate.
func (qc *QueueController) ApplyUpdate(update *p2ppb.QueueUpdate, localHub *Hub, node *Node, rm *roles.RoleManager) {
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

	if localHub != nil { // Notify local GUI client about the change
		localHub.Broadcast(qc.Snapshot())
	}

	// Trigger queue-to-runner glue
	node.ReactToQueueUpdate(qc, rm)
}
