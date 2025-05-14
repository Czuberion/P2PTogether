// peer_service/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"peer_service/internal/common"
	"peer_service/internal/media"
	"peer_service/internal/p2p"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"  // local import for client proto
	p2ppb "peer_service/proto/p2p" // local import for p2p proto
)

// server implements the gRPC service defined in proto.
type server struct {
	clientpb.UnimplementedP2PTClientServer
	hlsPort uint32

	// fan-out for ServerMsg broadcasts
	hub *p2p.Hub

	// owns the in-memory Queue and applies QueueCmds
	queueCtrl *p2p.QueueController

	// Peer runtime state
	node *p2p.Node

	// Role Management
	roleManager *roles.RoleManager

	// GossipSub control topic
	ctrlTopic *pubsub.Topic
}

func (s *server) GetServiceInfo(ctx context.Context, _ *emptypb.Empty) (*clientpb.ServiceInfo, error) {
	return &clientpb.ServiceInfo{HlsPort: s.hlsPort}, nil
}

// Send initial state (role definitions, all assignments, current queue) to a newly connected client.
func (s *server) sendInitialState(stream clientpb.P2PTClient_ControlStreamServer) {
	// 1. Send Role Definitions
	defs := s.roleManager.GetDefinitions()

	// Send Local Peer Identity first (or as part of initial state)
	identityMsg := &clientpb.LocalPeerIdentity{PeerId: s.node.ID().String()}
	if err := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_LocalPeerIdentity{LocalPeerIdentity: identityMsg}}); err != nil {
		log.Printf("Error sending LocalPeerIdentity to client for node %s: %v", s.node.ID(), err)
		// Decide if this is a fatal error for the stream
	}

	pbDefs := make([]*p2ppb.RoleDefinition, 0, len(defs))
	for _, d := range defs {
		pbDefs = append(pbDefs, &p2ppb.RoleDefinition{Name: d.Name, Permissions: uint32(d.Permissions)})
	}
	defUpdate := &p2ppb.RoleDefinitionsUpdate{Definitions: pbDefs, HlcTs: common.GetCurrentHLC()}
	_ = stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: defUpdate}}) // Error handling omitted for brevity

	// 2. Send All Peer Assignments
	assignmentsSnapshot := s.roleManager.GetAllAssignments()
	allAssignmentsMsg := &p2ppb.AllPeerRoleAssignments{
		Assignments: assignmentsSnapshot,
		HlcTs:       common.GetCurrentHLC(), // Timestamp for the snapshot itself
	}
	_ = stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_AllPeerRoleAssignments{AllPeerRoleAssignments: allAssignmentsMsg}})

	// 3. Send Current Queue State
	queueSnapshot := s.queueCtrl.Snapshot() // Assuming Snapshot() is public or accessible
	if qu, ok := queueSnapshot.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
		qu.QueueUpdate.HlcTs = common.GetCurrentHLC() // Add HLC to queue snapshot if it has a place for it
	}
	_ = stream.Send(queueSnapshot)

	log.Printf("Sent initial state (roles, queue) to client for node %s", s.node.ID())
}

func (s *server) ControlStream(stream clientpb.P2PTClient_ControlStreamServer) error {
	nodeIDStr := s.node.ID().String()
	s.hub.Add(nodeIDStr, stream)
	log.Printf("Client connection associated with node %s added to hub.", nodeIDStr)

	// Send initial state upon connection
	s.sendInitialState(stream)

	defer func() {
		// s.hub.Remove(pid.String())
		// log.Printf("Client %s removed from hub.", pid.String()) // Log removal
		s.hub.Remove(nodeIDStr)
		log.Printf("Client connection associated with node %s removed from hub.", nodeIDStr)
	}()

	for {
		clientMsg, err := stream.Recv()
		if err != nil {
			log.Printf("Client associated with node %s disconnected: %v", nodeIDStr, err)
			return err // Return error to signal stream closure
		}

		// Check if the stream's context has been cancelled (e.g., client disconnected or server shutting down)
		if stream.Context().Err() != nil {
			log.Printf("ControlStream for %s: stream context error: %v. Exiting handler.", nodeIDStr, stream.Context().Err())
			return stream.Context().Err()
		}

		// Determine Sender Permissions for commands originating from this GUI client
		senderID := s.node.ID()
		senderPerms := s.roleManager.GetPermissionsForPeer(senderID)

		// Log received command type and HLC for debugging
		var hlc int64
		var cmdType string
		if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_QueueCmd); ok {
			hlc = cmdPl.QueueCmd.HlcTs
			cmdType = "QueueCmd"
		}
		if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_SetPeerRolesCmd); ok {
			hlc = cmdPl.SetPeerRolesCmd.HlcTs
			cmdType = "SetPeerRolesCmd"
		}
		if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_UpdateRoleCmd); ok {
			hlc = cmdPl.UpdateRoleCmd.HlcTs
			cmdType = "UpdateRoleCmd"
		}
		if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_RemoveRoleCmd); ok {
			hlc = cmdPl.RemoveRoleCmd.HlcTs
			cmdType = "RemoveRoleCmd"
		}
		if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_PlaybackStateCmd); ok {
			hlc = cmdPl.PlaybackStateCmd.HlcTs
			cmdType = "SetPlaybackStateCmd"
		} // Updated
		log.Printf("[gRPC Recv %s] Payload: %s, HLC: %d", nodeIDStr, cmdType, hlc)

		switch cmd := clientMsg.Payload.(type) {
		case *clientpb.ClientMsg_QueueCmd:

			// Pass context from stream for cancellation propagation if Publish blocks.
			// Pass s.node and s.ctrlTopic for Handle to use.
			// if err := s.queueCtrl.Handle(stream.Context(), x.QueueCmd, pid, perms, s.hub, s.node, s.ctrlTopic); err != nil {
			// 	log.Printf("Error handling QueueCmd from %s: %v", pid.String(), err)

			// Pass the node's ID as the 'sender' for commands originating from the local gRPC client.

			// Permission check now happens inside Handle using RoleManager
			if err := s.queueCtrl.Handle(stream.Context(), cmd.QueueCmd, s.node.ID(), s.roleManager, s.hub, s.node, s.ctrlTopic); err != nil {
				log.Printf("Error handling QueueCmd (HLC: %d) from local client (node %s): %v", cmd.QueueCmd.HlcTs, nodeIDStr, err)
				// Decide if the error is fatal for this stream
				// return err // Example: return error to close stream on failure
			}

		case *clientpb.ClientMsg_PlaybackStateCmd: // Updated for SetPlaybackStateCmd
			pbCmd := cmd.PlaybackStateCmd
			// Permission Check: Any relevant playback permission allows sending state updates
			permissionOK := senderPerms.Has(roles.PermPlayPause) ||
				senderPerms.Has(roles.PermSeek) ||
				senderPerms.Has(roles.PermSetSpeed)

			if !permissionOK {
				log.Printf("[gRPC Denied %s] SetPlaybackStateCmd denied. Missing required playback permission. HLC: %d", nodeIDStr, pbCmd.HlcTs)
				continue // Ignore command if permission denied
			}

			// Wrap in ServerMsg and broadcast via GossipSub
			serverPl := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaybackStateCmd{PlaybackStateCmd: pbCmd}} // Updated ServerMsg field
			if err := s.publishToControlTopic(stream.Context(), serverPl); err != nil {
				log.Printf("[gRPC Error %s] Failed to publish SetPlaybackStateCmd to ctrlTopic: %v. HLC: %d", nodeIDStr, err, pbCmd.HlcTs)
			}

		case *clientpb.ClientMsg_SetPeerRolesCmd:
			log.Printf("Received SetPeerRolesCmd (HLC: %d) from %s for target %s", cmd.SetPeerRolesCmd.HlcTs, nodeIDStr, cmd.SetPeerRolesCmd.TargetPeerId)
			s.handleSetPeerRolesCmd(stream.Context(), cmd.SetPeerRolesCmd, s.node.ID()) // Assuming s.node.ID() is the sender for now

		case *clientpb.ClientMsg_UpdateRoleCmd:
			log.Printf("Received UpdateRoleCmd (HLC: %d) from %s for role %s", cmd.UpdateRoleCmd.HlcTs, nodeIDStr, cmd.UpdateRoleCmd.Definition.Name)
			s.handleUpdateRoleCmd(stream.Context(), cmd.UpdateRoleCmd, s.node.ID())

		case *clientpb.ClientMsg_RemoveRoleCmd:
			log.Printf("Received RemoveRoleCmd (HLC: %d) from %s for role %s", cmd.RemoveRoleCmd.HlcTs, nodeIDStr, cmd.RemoveRoleCmd.RoleName)
			s.handleRemoveRoleCmd(stream.Context(), cmd.RemoveRoleCmd, s.node.ID())

		default:
			log.Printf("Received unhandled message type from %s", nodeIDStr)
		}

		if stream.Context().Err() != nil {
			log.Printf("ControlStream for %s: stream context cancelled. Exiting.", nodeIDStr)
			return stream.Context().Err()
		}
	}
}

// Helper to publish any ServerMsg payload to the control topic
func (s *server) publishToControlTopic(ctx context.Context, msg *clientpb.ServerMsg) error {
	if s.ctrlTopic == nil {
		return fmt.Errorf("control topic is nil, cannot publish")
	}
	marshalledMsg, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal ServerMsg for broadcast: %w", err)
	}
	if err := s.ctrlTopic.Publish(ctx, marshalledMsg); err != nil {
		return fmt.Errorf("failed to publish ServerMsg to ctrlTopic: %w", err)
	}
	return nil
}

// Handle incoming SetPeerRolesCmd
func (s *server) handleSetPeerRolesCmd(ctx context.Context, cmd *p2ppb.SetPeerRolesCmd, senderID peer.ID) {
	// 1. Check if sender has permission to manage roles
	senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
	if !senderPerms.Has(roles.PermManageUserRoles) {
		log.Printf("Permission denied: Peer %s lacks PermManageUserRoles to set roles for %s (Cmd HLC: %d)", senderID, cmd.TargetPeerId, cmd.HlcTs)
		// Optionally send an error back to the sender via gRPC stream?
		return
	}

	// 2. Decode target peer ID and call RoleManager
	// targetPeerID, err := peer.Decode(cmd.TargetPeerId)
	// if err != nil {
	// 	log.Printf("Failed to decode target peer ID '%s' for SetPeerRolesCmd: %v", cmd.TargetPeerId, err)
	// 	return
	// }

	// assignmentMsg, err := s.roleManager.SetPeerRoles(targetPeerID, cmd.AssignedRoleNames)

	// If the command's target_peer_id is the special "defaultPeerId" (or matches senderID),
	// it means the client is trying to change its own roles (the roles of the node this service instance represents).
	// In this case, the actual target for RoleManager is senderID (s.node.ID()).
	actualTargetPeerID := senderID // By default, assume sender is changing their own roles.

	// If cmd.TargetPeerId were a *different, valid* peer ID, we'd decode and use that.
	// For now, since GUI sends "defaultPeerId" for self, and senderID is s.node.ID(),
	// we use senderID as the target for RoleManager.SetPeerRoles.
	// This block can be expanded if GUI starts sending actual other peer IDs.

	assignmentMsg, err := s.roleManager.SetPeerRoles(actualTargetPeerID, cmd.AssignedRoleNames)
	if err != nil {
		// log.Printf("Error setting peer roles for %s: %v", targetPeerID, err)
		log.Printf("Error setting peer roles for %s: %v", actualTargetPeerID, err)
		// Optionally send an error back to the client
		return
	}

	if assignmentMsg != nil { // If there was a change
		assignmentMsg.HlcTs = common.GetCurrentHLC() // Set HLC timestamp on the generated message
		// log.Printf("Broadcasting PeerRoleAssignment for %s", targetPeerID)
		log.Printf("Broadcasting PeerRoleAssignment for %s", actualTargetPeerID)
		serverPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: assignmentMsg}}
		s.hub.Broadcast(serverPayload)
		_ = s.publishToControlTopic(ctx, serverPayload)
	}
}

func (s *server) handleUpdateRoleCmd(ctx context.Context, cmd *p2ppb.UpdateRoleCmd, senderID peer.ID) {
	senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
	if !senderPerms.Has(roles.PermAddRemoveRoles) { // Example permission
		log.Printf("Permission denied: Peer %s lacks PermAddRemoveRoles to update role definitions (Cmd HLC: %d)", senderID, cmd.HlcTs)
		return
	}

	if cmd.Definition == nil {
		log.Printf("Error handling UpdateRoleCmd: Definition is nil (Cmd HLC: %d)", cmd.HlcTs)
		return
	}

	definitionsUpdateMsg, err := s.roleManager.UpdateDefinition(cmd.Definition.Name, roles.Permission(cmd.Definition.Permissions))
	if err != nil {
		log.Printf("Error updating role definition '%s': %v (Cmd HLC: %d)", cmd.Definition.Name, err, cmd.HlcTs)
		return
	}

	if definitionsUpdateMsg != nil {
		definitionsUpdateMsg.HlcTs = common.GetCurrentHLC()
		log.Printf("Broadcasting RoleDefinitionsUpdate after role '%s' updated", cmd.Definition.Name)
		serverPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: definitionsUpdateMsg}}
		s.hub.Broadcast(serverPayload)
		_ = s.publishToControlTopic(ctx, serverPayload)
	}
}

func (s *server) handleRemoveRoleCmd(ctx context.Context, cmd *p2ppb.RemoveRoleCmd, senderID peer.ID) {
	senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
	if !senderPerms.Has(roles.PermAddRemoveRoles) {
		log.Printf("Permission denied: Peer %s lacks PermAddRemoveRoles to remove role definitions (Cmd HLC: %d)", senderID, cmd.HlcTs)
		return
	}

	definitionsUpdateMsg, affectedAssignments, err := s.roleManager.RemoveDefinition(cmd.RoleName)
	if err != nil {
		log.Printf("Error removing role definition '%s': %v (Cmd HLC: %d)", cmd.RoleName, err, cmd.HlcTs)
		return
	}

	if definitionsUpdateMsg != nil { // Should always be non-nil on success from RemoveDefinition
		definitionsUpdateMsg.HlcTs = common.GetCurrentHLC()
		log.Printf("Broadcasting RoleDefinitionsUpdate after role '%s' removed", cmd.RoleName)
		serverPayloadDefs := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: definitionsUpdateMsg}}

		s.hub.Broadcast(serverPayloadDefs)
		_ = s.publishToControlTopic(ctx, serverPayloadDefs)
	}

	for _, assignmentMsg := range affectedAssignments {
		assignmentMsg.HlcTs = common.GetCurrentHLC()
		log.Printf("Broadcasting PeerRoleAssignment update for peer %s after role '%s' removed", assignmentMsg.PeerId, cmd.RoleName)
		serverPayloadAssign := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: assignmentMsg}}

		s.hub.Broadcast(serverPayloadAssign)
		_ = s.publishToControlTopic(ctx, serverPayloadAssign)
	}
}

// mdnsNotifee prints every peer it discovers on the LAN.
type mdnsNotifee struct{ h host.Host }

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() {
		return // ignore ourselves
	}
	log.Printf("[mDNS] discovered %s\n", pi.ID)
	// Fire‑and‑forget dial; gossipsub attaches once the connection is up.
	go func() {
		if err := n.h.Connect(context.Background(), pi); err != nil {
			log.Printf("[mDNS] dial %s failed: %v", pi.ID, err)
		}
	}()
}

// buildHost sets up a real libp2p Host plus mDNS discovery.
func buildHost(ctx context.Context) host.Host {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip6/::/tcp/0",
		),
	)
	if err != nil {
		log.Fatalf("libp2p host init failed: %v", err)
	}

	// mDNS for zero-conf LAN discovery
	mdnsSvc := mdns.NewMdnsService(h, "p2ptogether-mdns", &mdnsNotifee{h: h})
	if err := mdnsSvc.Start(); err != nil {
		log.Fatalf("mDNS start failed: %v", err)
	}

	return h
}

// processControlTopicMessages handles all incoming ServerMsg messages from the ctrlTopic.
func processControlTopicMessages(ctx context.Context, sub *pubsub.Subscription, qc *p2p.QueueController, rm *roles.RoleManager, myID peer.ID, localHub *p2p.Hub, node *p2p.Node) {
	log.Println("[gossip] processControlTopicMessages started for peer", myID)
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("[gossip] pumpQueueUpdates: context cancelled, exiting.")
				return
			}
			log.Printf("[gossip] processControlTopicMessages: subscription error: %v", err)
			return // Exit on other errors too for simplicity
		}

		if msg.ReceivedFrom == myID {
			continue // Skip messages broadcast by self
		}

		var serverMsg clientpb.ServerMsg
		if err := proto.Unmarshal(msg.Data, &serverMsg); err != nil {
			log.Printf("[gossip] processControlTopicMessages: failed to unmarshal ServerMsg from %s: %v", msg.ReceivedFrom, err)
			continue
		}

		switch payload := serverMsg.GetPayload().(type) {
		case *clientpb.ServerMsg_QueueUpdate:
			handleGossipQueueUpdate(payload.QueueUpdate, msg.ReceivedFrom, qc, rm, localHub, node)
		case *clientpb.ServerMsg_RoleDefinitionsUpdate:
			handleGossipRoleDefinitionsUpdate(payload.RoleDefinitionsUpdate, msg.ReceivedFrom, rm, localHub, node, qc)
		case *clientpb.ServerMsg_PeerRoleAssignment:
			handleGossipPeerRoleAssignment(payload.PeerRoleAssignment, msg.ReceivedFrom, rm, localHub, node, qc, myID)
		case *clientpb.ServerMsg_AllPeerRoleAssignments:
			handleGossipAllPeerRoleAssignments(payload.AllPeerRoleAssignments, msg.ReceivedFrom, rm, localHub, node, qc)
		case *clientpb.ServerMsg_PlaybackStateCmd:
			handleGossipPlaybackStateCmd(payload.PlaybackStateCmd, msg.ReceivedFrom, localHub)
		default:
			log.Printf("[gossip] processControlTopicMessages: received unhandled ServerMsg payload type %T from %s", payload, msg.ReceivedFrom)
		}
	}
}

func handleGossipQueueUpdate(update *p2ppb.QueueUpdate, from peer.ID, qc *p2p.QueueController, rm *roles.RoleManager, hub *p2p.Hub, node *p2p.Node) {
	log.Printf("[gossip] handleGossipQueueUpdate: received from %s (HLC: %d)", from, update.HlcTs)
	qc.ApplyUpdate(update, hub, node, rm) // ApplyUpdate in QC needs rm for ReactToQueueUpdate
}

func handleGossipRoleDefinitionsUpdate(update *p2ppb.RoleDefinitionsUpdate, from peer.ID, rm *roles.RoleManager, hub *p2p.Hub, node *p2p.Node, qc *p2p.QueueController) {
	log.Printf("[gossip] handleGossipRoleDefinitionsUpdate: received from %s (HLC: %d)", from, update.HlcTs)
	if err := rm.ApplyDefinitionsUpdate(update); err != nil {
		log.Printf("[gossip] Error applying RoleDefinitionsUpdate from %s: %v", from, err)
	} else {
		// Successfully applied. Notify local GUI clients about the change in definitions.
		log.Printf("[gossip] Applied RoleDefinitionsUpdate from %s. Broadcasting to local hub.", from)
		serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: update}}
		hub.Broadcast(serverMsg)
		// Note: Applying new definitions might change effective permissions for *many* peers.
		// A more advanced system might re-evaluate and broadcast individual PeerRoleAssignments
		// if effective permissions changed, or simply expect clients to re-calculate.
		// For now, also re-evaluate this node's runner state as its permissions might have changed.
		// This is a broad action; finer-grained reactions could be implemented.
		if node != nil && qc != nil { // qc needed by ReactToQueueUpdate
			node.ReactToQueueUpdate(qc, rm)
		}
	}
}

func handleGossipPeerRoleAssignment(assignment *p2ppb.PeerRoleAssignment, from peer.ID, rm *roles.RoleManager, hub *p2p.Hub, node *p2p.Node, qc *p2p.QueueController, myID peer.ID) {
	log.Printf("[gossip] handleGossipPeerRoleAssignment: for peer %s from %s (HLC: %d)", assignment.PeerId, from, assignment.HlcTs)
	targetPeerID, err := peer.Decode(assignment.PeerId)
	if err != nil {
		log.Printf("[gossip] Invalid peer ID '%s' in PeerRoleAssignment from %s: %v", assignment.PeerId, from, err)
		return
	}
	changed, err := rm.ApplyPeerAssignment(targetPeerID, assignment.AssignedRoleNames, assignment.HlcTs)
	if err != nil {
		log.Printf("[gossip] Error applying PeerRoleAssignment for %s from %s: %v", targetPeerID, from, err)
	} else if changed {
		log.Printf("[gossip] Applied PeerRoleAssignment for %s from %s.", targetPeerID, from)
		// Notify local GUI about this specific assignment change
		serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: assignment}}
		hub.Broadcast(serverMsg)
		if targetPeerID == myID && node != nil && qc != nil { // If this node's roles changed
			log.Printf("[gossip] Own roles changed due to PeerRoleAssignment from %s. Re-evaluating runner.", from)
			node.ReactToQueueUpdate(qc, rm)
		}
	}
}

func handleGossipAllPeerRoleAssignments(snapshot *p2ppb.AllPeerRoleAssignments, from peer.ID, rm *roles.RoleManager, hub *p2p.Hub, node *p2p.Node, qc *p2p.QueueController) {
	log.Printf("[gossip] handleGossipAllPeerRoleAssignments: received from %s (HLC: %d)", from, snapshot.HlcTs)
	if err := rm.ApplyAllAssignmentsSnapshot(snapshot); err != nil {
		log.Printf("[gossip] Error applying AllPeerRoleAssignments from %s: %v", from, err)
	} else {
		log.Printf("[gossip] Applied AllPeerRoleAssignments from %s. Broadcasting to local hub and re-evaluating runner.", from)
		serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_AllPeerRoleAssignments{AllPeerRoleAssignments: snapshot}}
		hub.Broadcast(serverMsg)
		if node != nil && qc != nil {
			node.ReactToQueueUpdate(qc, rm)
		}
	}
}

// handleGossipPlaybackStateCmd forwards a SetPlaybackStateCmd received via GossipSub to the local GUI via the Hub.
func handleGossipPlaybackStateCmd(cmd *p2ppb.SetPlaybackStateCmd, from peer.ID, hub *p2p.Hub) {
	log.Printf("[gossip PlaybackRecv] Received SetPlaybackStateCmd (Play:%v Pos:%.2f Speed:%.2f HLC:%d) from %s. Broadcasting to local hub.", cmd.TargetIsPlaying, cmd.TargetTimePos, cmd.TargetSpeed, cmd.HlcTs, from)
	serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaybackStateCmd{PlaybackStateCmd: cmd}} // Updated ServerMsg field
	hub.Broadcast(serverMsg)                                                                               // Send to the connected GUI client(s)
}

// processSegmentEvents listens for new segments from the local RingBuffer (written by ffmpeg via IngestHandler),
// checks if this node is the designated streamer, and if so, publishes the segment via GossipSub.
func (s *server) processSegmentEvents(ctx context.Context, rb *media.RingBuffer) {
	log.Println("[SegmentProcessor] Starting segment event processor...")
	eventChan := rb.GetSegmentEventChannel()

	for {
		select {
		case <-ctx.Done():
			log.Println("[SegmentProcessor] Context cancelled, stopping segment event processor.")
			return
		case event := <-eventChan:
			// This node's ffmpeg produced a segment and it landed in the RingBuffer.
			// Decision Q2: "Only owner peer runs ffmpeg locally" implies this node IS the streamer.
			// We still do a sanity check against roles and queue state.
			isDesignatedStreamer := false
			headItem, headOk := s.queueCtrl.Q().Head()
			if headOk {
				localPeerID := s.node.ID()
				permissions := s.roleManager.GetPermissionsForPeer(localPeerID)
				if headItem.StoredBy == localPeerID && permissions.Has(roles.PermStream) {
					// Optional: More detailed check if event.Seq aligns with expected sequence for headItem
					// and if headItem.FilePath matches the one EncoderRunner is using.
					isDesignatedStreamer = true
				}
			}

			if !isDesignatedStreamer {
				log.Printf("[SegmentProcessor] Segment %d from local RingBuffer, but node (%s) is not designated streamer. NOT publishing.", event.Seq, s.node.ID())
				continue
			}

			segmentData := rb.Get(event.Seq) // Retrieve segment data from RingBuffer
			if segmentData == nil {
				log.Printf("[SegmentProcessor] Segment %d not found in RingBuffer after event. Skipping publish.", event.Seq)
				continue
			}

			videoMsg := &p2ppb.VideoSegmentGossip{ // Use the p2ppb alias
				SequenceNumber: event.Seq,
				Data:           segmentData,
			}
			marshalledVideoMsg, err := proto.Marshal(videoMsg)
			if err != nil {
				log.Printf("[gossip VideoPublish] Failed to marshal VideoSegmentGossip for seq %d: %v", event.Seq, err)
				continue
			}

			if videoTopic := s.node.VideoTopic(); videoTopic != nil {
				if err := videoTopic.Publish(context.Background(), marshalledVideoMsg); err != nil { // Using context.Background for publish from this long-running goroutine
					log.Printf("[gossip VideoPublish] Failed to publish video segment %d: %v", event.Seq, err)
				} else {
					log.Printf("[gossip VideoPublish] Published video segment %d (%d bytes) via event processor", event.Seq, len(segmentData))
					s.node.AddBytesUp(uint64(len(marshalledVideoMsg)))
				}
			}
		}
	}
}

func (s *server) processVideoSegmentMessages(ctx context.Context, sub *pubsub.Subscription, rb *media.RingBuffer) {
	log.Println("[gossip VideoReceive] Started processing incoming video segment messages for peer", s.node.ID())
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("[gossip VideoReceive] Context cancelled, exiting video segment processor.")
				return
			}
			log.Printf("[gossip VideoReceive] Subscription error: %v", err)
			return // Consider retry or cleaner exit
		}

		if msg.ReceivedFrom == s.node.ID() {
			// Optional: log if you want to see self-originated messages being ignored
			// log.Printf("[gossip VideoReceive] Ignoring self-originated video segment.")
			continue // Ignore messages broadcast by self
		}

		var videoSegment p2ppb.VideoSegmentGossip // Use the p2ppb alias
		if err := proto.Unmarshal(msg.Data, &videoSegment); err != nil {
			log.Printf("[gossip VideoReceive] Failed to unmarshal VideoSegmentGossip from %s: %v", msg.ReceivedFrom, err)
			continue
		}

		// --- Permission Check (PermView) ---
		// This peer (s.node.ID()) needs PermView to store and play the segment
		localPeerPermissions := s.roleManager.GetPermissionsForPeer(s.node.ID())
		if !localPeerPermissions.Has(roles.PermView) {
			log.Printf("[gossip VideoReceive] No PermView for local peer %s. Dropping video segment %d from %s.", s.node.ID(), videoSegment.SequenceNumber, msg.ReceivedFrom)
			continue
		}

		// --- Store Segment in Local RingBuffer ---
		// Disable events from the RingBuffer when writing segments received from gossip
		// to prevent re-publishing them if this node *also* happens to be the streamer
		// (though that's less likely if only one streamer, but good for safety).
		rb.SetPublishEvents(false)
		rb.WriteAt(videoSegment.SequenceNumber, videoSegment.Data, 0 /*pts placeholder*/)
		rb.SetPublishEvents(true) // Re-enable for segments from local ffmpeg

		log.Printf("[gossip VideoReceive] Received and stored video segment %d (%d bytes) from %s.", videoSegment.SequenceNumber, len(videoSegment.Data), msg.ReceivedFrom)
		s.node.AddBytesDown(uint64(len(msg.Data))) // Track incoming gossip traffic
	}
}

func main() {
	// flags
	var grpcPort int
	flag.IntVar(&grpcPort, "grpc-port", 0, "gRPC listen port")
	flag.Parse()

	// 1) Start HTTP mini-HLS on a random port
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("HTTP listen failed: %v", err)
	}
	hlsPort := uint32(httpLn.Addr().(*net.TCPAddr).Port)
	log.Printf("HLS endpoint listening on %s\n", httpLn.Addr().String())

	// --- mini‑HLS in‑RAM buffer ---
	rb := media.NewRingBuffer(120) // 120 s window
	statusCh := media.StartStatusTicker(rb, 5*time.Second)
	plH, segH, triggerDiscontinuity := media.Handler(rb)

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/stream.m3u8", plH)
	// httpMux.HandleFunc("/seg_", segH) // matches /seg_<seq>.ts
	httpMux.HandleFunc("/ingest/", media.IngestHandler(rb))
	httpMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/seg_") && strings.HasSuffix(r.URL.Path, ".ts"):
			segH(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	httpServer := &http.Server{Addr: httpLn.Addr().String(), Handler: httpMux}

	// Goroutine to start the HLS server
	go func() {
		log.Println("Starting HLS server...")
		if err := httpServer.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HLS server error: %v", err)
		}
		log.Println("HLS server goroutine finished.") // Add log
	}()

	// 2) Start gRPC server
	grpcLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", grpcPort))
	if err != nil {
		log.Fatalf("gRPC listen failed: %v", err)
	}
	grpcServer := grpc.NewServer()
	hub := p2p.NewHub()
	queueCtrl := p2p.NewQueueController()
	roleManager := roles.NewRoleManager()

	// 3) libp2p + GossipSub

	ctx, cancelCtx := context.WithCancel(context.Background())

	lhost := buildHost(ctx)
	node := p2p.NewNode(lhost, hlsPort)

	// Assign default roles (e.g., "Admin" or "Streamer,Viewer") to the service's own node ID
	// This allows the service itself (when acting as sender) to have permissions.
	// For testing self-role changes via GUI, it needs PermManageUserRoles if target is self.
	// Giving Admin role to self for now for full testing capability.
	_, err = roleManager.SetPeerRoles(node.ID(), []string{"Admin"}) // Assign "Admin" to self
	if err != nil {
		log.Fatalf("Failed to set initial self-roles for service node: %v", err)
	}
	log.Printf("Service node %s initialized with Admin role in RoleManager.", node.ID())

	ps, err := pubsub.NewGossipSub(ctx, lhost)
	if err != nil {
		log.Fatalf("GossipSub init failed: %v", err)
	}

	videoTopic, err := ps.Join("/p2ptogether/video/1")
	if err != nil {
		log.Fatalf("join video topic: %v", err)
	}
	// session‑scoped topics – stub “default” until sessions exist
	chatTopic, _ := ps.Join("/p2ptogether/chat/default")
	ctrlTopic, _ := ps.Join("/p2ptogether/control/default")

	node.AttachPubSub(videoTopic, chatTopic, ctrlTopic)

	// Subscribe to the control topic for various ServerMsg types
	subCtrl, err := ctrlTopic.Subscribe()
	if err != nil {
		log.Fatalf("ctrl topic subscribe: %v", err)
	}
	go processControlTopicMessages(ctx, subCtrl, queueCtrl, roleManager, lhost.ID(), hub, node)

	//  Peer‑connected / peer‑lost trace
	sub, err := lhost.EventBus().Subscribe(
		[]interface{}{new(event.EvtPeerConnectednessChanged)})
	if err != nil {
		log.Fatalf("event‑bus subscribe failed: %v", err)
	}
	go func() {
		for ev := range sub.Out() {
			e := ev.(event.EvtPeerConnectednessChanged)
			switch e.Connectedness {
			case network.Connected:
				log.Printf("[conn] ✔ peer connected: %s", e.Peer)
			case network.NotConnected:
				log.Printf("[conn] ✖ peer lost: %s", e.Peer)
			default:
				log.Printf("[conn] ↻ peer %s changed: %v", e.Peer, e.Connectedness)
			}
		}
	}()

	// clientpb.RegisterP2PTClientServer(grpcServer, &server{
	// Define the server instance that holds all dependencies
	srvInstance := &server{
		hlsPort:     hlsPort,
		hub:         hub,
		queueCtrl:   queueCtrl,
		node:        node,
		roleManager: roleManager,
		ctrlTopic:   ctrlTopic,
	}
	clientpb.RegisterP2PTClientServer(grpcServer, srvInstance)

	subVideo, err := node.VideoTopic().Subscribe()
	if err != nil {
		log.Fatalf("video topic subscribe for receiving failed: %v", err)
	}
	go srvInstance.processVideoSegmentMessages(ctx, subVideo, rb) // srvInstance has node, roleManager

	// Start the segment event processor goroutine
	go srvInstance.processSegmentEvents(ctx, rb)

	// fan‑out StreamStatus to every connected client
	go func() {
		for st := range statusCh {
			hub.Broadcast(&clientpb.ServerMsg{
				Payload: &clientpb.ServerMsg_StreamStatus{StreamStatus: st},
			})
		}
	}()

	// Goroutine to start the gRPC server
	go func() {
		log.Printf("gRPC server listening on %s\n", grpcLn.Addr().String())
		if err := grpcServer.Serve(grpcLn); err != nil {
			// Log non-fatal error if Serve returns after GracefulStop
			// Example error string: "grpc: the server has been stopped"
			if err.Error() != "grpc: the server has been stopped" {
				log.Printf("gRPC serve failed: %v", err)
			}
		}
		log.Println("gRPC server goroutine finished.") // Add log
	}()

	// 4) Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	sig := <-stopChan                                                // Block until a signal is received
	log.Printf("Received signal: %v. Shutting down servers...", sig) // Log received signal

	// Cancel the main context to signal background goroutines
	log.Println("Cancelling main context...")
	cancelCtx()

	// Shutdown gRPC server - Use Stop() instead of GracefulStop()
	log.Println("Attempting gRPC server Stop()...")
	grpcServer.Stop()                            // Force immediate stop
	log.Println("gRPC server Stop() completed.") // Log completion

	_ = triggerDiscontinuity // (future use when swapping streamers)

	// Shutdown HTTP server
	log.Println("Attempting HTTP server Shutdown()...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	} else {
		log.Println("HTTP server shutdown complete.")
	}

	// Add a small delay to see if goroutines finish and logs appear
	log.Println("Waiting briefly before final exit...")
	time.Sleep(2 * time.Second)

	log.Println("Peer service exited gracefully.")
}
