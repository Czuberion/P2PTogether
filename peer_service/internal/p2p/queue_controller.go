package p2p

import (
	"fmt"
	"peer_service/internal/roles"
	pb "peer_service/proto/client"

	"github.com/libp2p/go-libp2p/core/peer"
)

type QueueController struct {
	q *Queue
}

func NewQueueController() *QueueController { return &QueueController{q: &Queue{}} }

func (qc *QueueController) snapshot() *pb.ServerMsg {
	it := qc.q.Items()
	pbItems := make([]*pb.QueueItem, 0, len(it))
	for _, x := range it {
		pbItems = append(pbItems, &pb.QueueItem{
			FilePath: x.FilePath,
			StoredBy: x.StoredBy.String(),
			AddedBy:  x.AddedBy.String(),
			HlcTs:    x.HlcTs,
		})
	}
	return &pb.ServerMsg{
		Payload: &pb.ServerMsg_QueueUpdate{QueueUpdate: &pb.QueueUpdate{Items: pbItems}},
	}
}

func (qc *QueueController) Handle(cmd *pb.QueueCmd, sender peer.ID, perms roles.Permission, hub *Hub) error {
	if !roles.HasPermission(roles.PermQueue, perms) {
		return fmt.Errorf("permission denied: missing Queue perm")
	}

	switch cmd.Type {
	case pb.QueueCmd_APPEND:
		qc.q.Append(QueueItem{
			FilePath: cmd.FilePath,
			StoredBy: sender,
			AddedBy:  sender,
			HlcTs:    cmd.HlcTs,
		})
	case pb.QueueCmd_REMOVE:
		qc.q.Remove(int(cmd.Index))
	case pb.QueueCmd_CLEAR:
		qc.q.Clear()
	case pb.QueueCmd_NEXT:
		qc.q.PopHead()
	}

	// after any successful mutation
	hub.Broadcast(qc.snapshot())
	return nil
}
