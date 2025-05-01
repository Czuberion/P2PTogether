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

func (qc *QueueController) Handle(cmd *pb.QueueCmd, sender peer.ID, perms roles.Permission) error {
	if !roles.HasPermission(roles.Queue, []roles.Role{{Permissions: perms}}) {
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
	// TODO: broadcast queue update to GUI
	return nil
}
