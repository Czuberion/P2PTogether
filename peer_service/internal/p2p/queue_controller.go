package p2p

import (
	"fmt"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"

	"github.com/libp2p/go-libp2p/core/peer"
)

type QueueController struct {
	q *Queue
}

func NewQueueController() *QueueController { return &QueueController{q: &Queue{}} }

func (qc *QueueController) snapshot() *clientpb.ServerMsg {
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

func (qc *QueueController) Handle(cmd *p2ppb.QueueCmd, sender peer.ID, perms roles.Permission, hub *Hub) error {
	if !roles.HasPermission(roles.PermQueue, perms) {
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

	// after any successful mutation
	hub.Broadcast(qc.snapshot())
	return nil
}
