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
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
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

	// GossipSub chat topic
	chatTopic *pubsub.Topic

	triggerHLSDircontinuity func()
}

func (s *server) GetServiceInfo(ctx context.Context, _ *emptypb.Empty) (*clientpb.ServiceInfo, error) {
	return &clientpb.ServiceInfo{HlsPort: s.hlsPort}, nil
}

// Send initial state (role definitions, all assignments, current queue) to a newly connected client.
func (s *server) sendInitialState(stream clientpb.P2PTClient_ControlStreamServer) {
	// 1. Send Local Peer Identity first
	identityMsg := &clientpb.LocalPeerIdentity{PeerId: s.node.ID().String()}
	if err := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_LocalPeerIdentity{LocalPeerIdentity: identityMsg}}); err != nil {
		log.Printf("Error sending LocalPeerIdentity to client for node %s: %v", s.node.ID(), err)
		return // If this fails, further state is problematic
	}

	// 2. Send Role Definitions
	defs := s.roleManager.GetDefinitions()

	pbDefs := make([]*p2ppb.RoleDefinition, 0, len(defs))
	for _, d := range defs {
		pbDefs = append(pbDefs, &p2ppb.RoleDefinition{Name: d.Name, Permissions: uint32(d.Permissions)})
	}
	defUpdate := &p2ppb.RoleDefinitionsUpdate{Definitions: pbDefs, HlcTs: common.GetCurrentHLC()}
	if err := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: defUpdate}}); err != nil {
		log.Printf("Error sending RoleDefinitionsUpdate: %v", err)
	}

	// 3. Send All Peer Assignments
	assignmentsSnapshot := s.roleManager.GetAllAssignments()
	allAssignmentsMsg := &p2ppb.AllPeerRoleAssignments{
		Assignments: assignmentsSnapshot,
		HlcTs:       common.GetCurrentHLC(),
	}
	if err := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_AllPeerRoleAssignments{AllPeerRoleAssignments: allAssignmentsMsg}}); err != nil {
		log.Printf("Error sending AllPeerRoleAssignments: %v", err)
	}

	// 4. Send Current Queue State
	queueSnapshotMsg := s.queueCtrl.Snapshot()
	if qu, ok := queueSnapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
		qu.QueueUpdate.HlcTs = common.GetCurrentHLC() // Add HLC to queue snapshot if it has a place for it
	}

	if err := stream.Send(queueSnapshotMsg); err != nil {
		log.Printf("Error sending QueueUpdate snapshot: %v", err)
	}

	log.Printf("Sent initial state (identity, roles, queue) to client for node %s", s.node.ID())
}

func (s *server) ControlStream(stream clientpb.P2PTClient_ControlStreamServer) error {
	nodeIDStr := s.node.ID().String()
	s.hub.Add(nodeIDStr, stream)
	log.Printf("Client connection associated with node %s added to hub.", nodeIDStr)

	// Send initial state upon connection
	s.sendInitialState(stream)

	defer func() {
		s.hub.Remove(nodeIDStr)
		log.Printf("Client connection associated with node %s removed from hub.", nodeIDStr)
	}()

	for {
		clientMsg, err := stream.Recv()
		if err != nil {
			if stream.Context().Err() != nil {
				log.Printf("ControlStream for %s: stream context error (likely client disconnected): %v", nodeIDStr, stream.Context().Err())
			} else {
				log.Printf("ControlStream for %s: Recv() error: %v", nodeIDStr, err)
			}
			return err
		}

		senderID := s.node.ID() // Commands from GUI are from this node

		var hlc int64
		var cmdTypeString string

		if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_QueueCmd); ok {
			hlc = cmdPl.QueueCmd.HlcTs
			cmdTypeString = fmt.Sprintf("QueueCmd (Type: %s, Path: %s, Index: %d)", cmdPl.QueueCmd.Type, cmdPl.QueueCmd.FilePath, cmdPl.QueueCmd.Index)
		} else if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_SetPeerRolesCmd); ok {
			hlc = cmdPl.SetPeerRolesCmd.HlcTs
			cmdTypeString = fmt.Sprintf("SetPeerRolesCmd (Target: %s, Roles: %v)", cmdPl.SetPeerRolesCmd.TargetPeerId, cmdPl.SetPeerRolesCmd.AssignedRoleNames)
		} else if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_UpdateRoleCmd); ok {
			hlc = cmdPl.UpdateRoleCmd.HlcTs
			cmdTypeString = fmt.Sprintf("UpdateRoleCmd (Name: %s, Perms: %d)", cmdPl.UpdateRoleCmd.Definition.Name, cmdPl.UpdateRoleCmd.Definition.Permissions)
		} else if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_RemoveRoleCmd); ok {
			hlc = cmdPl.RemoveRoleCmd.HlcTs
			cmdTypeString = fmt.Sprintf("RemoveRoleCmd (Name: %s)", cmdPl.RemoveRoleCmd.RoleName)
		} else if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_PlaybackStateCmd); ok {
			hlc = cmdPl.PlaybackStateCmd.HlcTs
			cmdTypeString = fmt.Sprintf("SetPlaybackStateCmd (Playing: %v, Pos: %.2f, Speed: %.2f)", cmdPl.PlaybackStateCmd.TargetIsPlaying, cmdPl.PlaybackStateCmd.TargetTimePos, cmdPl.PlaybackStateCmd.TargetSpeed)
		} else if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_PlaybackStreamCompletedCmd); ok {
			hlc = cmdPl.PlaybackStreamCompletedCmd.HlcTs
			cmdTypeString = fmt.Sprintf("PlaybackStreamCompletedCmd (StreamSeq: %d)", cmdPl.PlaybackStreamCompletedCmd.StreamSequenceId)
		} else {
			cmdTypeString = fmt.Sprintf("Unknown type %T", clientMsg.Payload)
		}
		log.Printf("[gRPC Recv %s] %s, HLC: %d", nodeIDStr, cmdTypeString, hlc)

		switch cmd := clientMsg.Payload.(type) {
		case *clientpb.ClientMsg_QueueCmd:
			if err := s.queueCtrl.Handle(stream.Context(), cmd.QueueCmd, senderID, s.roleManager, s.hub, s.node, s.ctrlTopic); err != nil {
				log.Printf("Error handling QueueCmd from %s: %v", nodeIDStr, err)
			}

		case *clientpb.ClientMsg_PlaybackStateCmd: // Updated for SetPlaybackStateCmd
			pbCmd := cmd.PlaybackStateCmd
			s.queueCtrl.UpdateLastKnownPlaybackState(pbCmd) // Update QC's view
			// Permission Check: Any relevant playback permission allows sending state updates
			senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
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
			s.handleSetPeerRolesCmd(stream.Context(), cmd.SetPeerRolesCmd, senderID)

		case *clientpb.ClientMsg_UpdateRoleCmd:
			log.Printf("Received UpdateRoleCmd (HLC: %d) from %s for role %s", cmd.UpdateRoleCmd.HlcTs, nodeIDStr, cmd.UpdateRoleCmd.Definition.Name)
			s.handleUpdateRoleCmd(stream.Context(), cmd.UpdateRoleCmd, senderID)

		case *clientpb.ClientMsg_RemoveRoleCmd:
			log.Printf("Received RemoveRoleCmd (HLC: %d) from %s for role %s", cmd.RemoveRoleCmd.HlcTs, nodeIDStr, cmd.RemoveRoleCmd.RoleName)
			s.handleRemoveRoleCmd(stream.Context(), cmd.RemoveRoleCmd, senderID)

		case *clientpb.ClientMsg_PlaybackStreamCompletedCmd:
			pbStreamCompletedCmd := cmd.PlaybackStreamCompletedCmd // This is a pointer
			log.Printf("[gRPC Recv %s] Processing PlaybackStreamCompletedCmd for stream %d, HLC: %d", nodeIDStr, pbStreamCompletedCmd.StreamSequenceId, pbStreamCompletedCmd.HlcTs)
			s.queueCtrl.NotifyPlayerStreamCompleted(pbStreamCompletedCmd.StreamSequenceId, senderID)

		default:
			log.Printf("[gRPC Recv %s] Unhandled ClientMsg payload type: %T", nodeIDStr, clientMsg.Payload)
		}

		// Check for stream context error after processing each message
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

// Helper to publish any ServerMsg (specifically ChatMsg) payload to the chat topic
func (s *server) publishToChatTopic(ctx context.Context, msg *clientpb.ServerMsg) error {
	if s.chatTopic == nil {
		return fmt.Errorf("chat topic is nil, cannot publish chat message")
	}
	marshalledMsg, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal ServerMsg (ChatMsg) for chat broadcast: %w", err)
	}
	if err := s.chatTopic.Publish(ctx, marshalledMsg); err != nil {
		return fmt.Errorf("failed to publish ServerMsg (ChatMsg) to chatTopic: %w", err)
	}
	return nil
}

// Handle incoming SetPeerRolesCmd
func (s *server) handleSetPeerRolesCmd(ctx context.Context, cmd *p2ppb.SetPeerRolesCmd, senderID peer.ID) {
	senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
	if !senderPerms.Has(roles.PermManageUserRoles) {
		log.Printf("Permission denied: Peer %s lacks PermManageUserRoles to set roles for %s (Cmd HLC: %d)", senderID, cmd.TargetPeerId, cmd.HlcTs)
		// Optionally send an error back to the sender via gRPC stream?
		return
	}

	actualTargetPeerID := senderID // Assuming GUI sends its own ID or a placeholder like "defaultPeerId" for self-change
	// If cmd.TargetPeerId is a valid peer.ID string for another peer, decode and use it.
	// For now, this logic assumes client wants to change ITS OWN roles.
	if cmd.TargetPeerId != senderID.String() && cmd.TargetPeerId != "defaultPeerId" && cmd.TargetPeerId != "" {
		decodedTarget, err := peer.Decode(cmd.TargetPeerId)
		if err == nil {
			actualTargetPeerID = decodedTarget
		} else {
			log.Printf("Warning: SetPeerRolesCmd from %s had invalid target_peer_id '%s'. Defaulting to sender.", senderID, cmd.TargetPeerId)
		}
	}

	assignmentMsg, err := s.roleManager.SetPeerRoles(actualTargetPeerID, cmd.AssignedRoleNames)
	if err != nil {
		log.Printf("Error setting peer roles for %s: %v", actualTargetPeerID, err)
		// Optionally send an error back to the client
		return
	}

	if assignmentMsg != nil { // If there was a change
		assignmentMsg.HlcTs = common.GetCurrentHLC() // Set HLC timestamp on the generated message
		log.Printf("Broadcasting PeerRoleAssignment for %s with HLC %d", actualTargetPeerID, assignmentMsg.HlcTs)
		serverPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: assignmentMsg}}
		s.hub.Broadcast(serverPayload)
		_ = s.publishToControlTopic(ctx, serverPayload)
	}
}

func (s *server) handleUpdateRoleCmd(ctx context.Context, cmd *p2ppb.UpdateRoleCmd, senderID peer.ID) {
	senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
	if !senderPerms.Has(roles.PermAddRemoveRoles) {
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
		log.Printf("Broadcasting RoleDefinitionsUpdate after role '%s' updated, HLC %d", cmd.Definition.Name, definitionsUpdateMsg.HlcTs)
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
		log.Printf("Broadcasting RoleDefinitionsUpdate after role '%s' removed, HLC %d", cmd.RoleName, definitionsUpdateMsg.HlcTs)
		serverPayloadDefs := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: definitionsUpdateMsg}}

		s.hub.Broadcast(serverPayloadDefs)
		_ = s.publishToControlTopic(ctx, serverPayloadDefs)
	}

	for _, assignmentMsg := range affectedAssignments {
		assignmentMsg.HlcTs = common.GetCurrentHLC()
		log.Printf("Broadcasting PeerRoleAssignment update for peer %s after role '%s' removed, HLC %d", assignmentMsg.PeerId, cmd.RoleName, assignmentMsg.HlcTs)
		serverPayloadAssign := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: assignmentMsg}}

		s.hub.Broadcast(serverPayloadAssign)
		_ = s.publishToControlTopic(ctx, serverPayloadAssign)
	}
}

// mdnsNotifee prints every peer it discovers on the LAN.
type mdnsNotifee struct {
	h   host.Host
	dht *dht.IpfsDHT
}

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() {
		return // ignore ourselves
	}
	log.Printf("[mDNS] discovered %s with addrs: %v\n", pi.ID, pi.Addrs)

	// Attempt to connect to the peer.
	// DHT bootstrap will happen after connection if it's a new peer.
	go func() {
		log.Printf("[mDNS] Attempting to connect to %s", pi.ID)
		if err := n.h.Connect(context.Background(), pi); err != nil {
			log.Printf("[mDNS] dial %s failed: %v", pi.ID, err)
		} else {
			log.Printf("[mDNS] successfully connected to %s", pi.ID)
			// If using DHT and connection is successful, DHT will naturally learn about this peer.
			// Explicitly adding to DHT's peerstore might not be necessary if AutoRelay/AutoNAT services are active
			// and the peer is routable, or if a bootstrap process runs.
		}
	}()
}

// buildHost sets up a real libp2p Host plus mDNS discovery and Kademlia DHT.
func buildHost(ctx context.Context) (host.Host, *dht.IpfsDHT) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic",
			"/ip6/::/tcp/0",
			"/ip6/::/udp/0/quic",
		),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),        // Enable p2p-circuit relay (both client and server)
		libp2p.EnableHolePunching(), // Enable hole punching
	)
	if err != nil {
		log.Fatalf("libp2p host init failed: %v", err)
	}

	// Initialize Kademlia DHT in server mode.
	// BootstrapPeers are usually hardcoded or discovered through other means (e.g., mDNS for local, then expanding).
	// For LAN, mDNS might be sufficient to find initial peers.
	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		log.Fatalf("Kademlia DHT init failed: %v", err)
	}

	// mDNS for zero-conf LAN discovery
	// Pass DHT to notifee if it needs to interact with it (e.g., add discovered peers)
	mdnsSvc := mdns.NewMdnsService(h, "p2ptogether-mdns", &mdnsNotifee{h: h, dht: kademliaDHT})
	if err := mdnsSvc.Start(); err != nil {
		log.Fatalf("mDNS start failed: %v", err)
	}

	// Bootstrap the DHT. In a real-world scenario, this would connect to predefined bootstrap nodes.
	// For LAN, we can rely on mDNS to find some initial peers, and then DHT can discover more.
	// Or, if you have known bootstrap peers on the LAN, add them here.
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		log.Printf("DHT bootstrap failed: %v (continuing, will rely on mDNS/connections)", err)
		// Don't fatal, as for LAN, mDNS might connect peers and DHT can build from there.
	}

	log.Printf("Libp2p Host created with ID: %s", h.ID().String())
	for _, addr := range h.Addrs() {
		log.Printf("Listening on address: %s/p2p/%s", addr, h.ID().String())
	}

	return h, kademliaDHT
}

// processControlTopicMessages handles all incoming ServerMsg messages from the ctrlTopic.
func processControlTopicMessages(ctx context.Context, sub *pubsub.Subscription, srv *server) {
	myID := srv.node.ID()
	log.Println("[gossip Ctrl] processControlTopicMessages started for peer", myID)
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("[gossip Ctrl] pumpQueueUpdates: context cancelled, exiting.")
				return
			}
			log.Printf("[gossip Ctrl] processControlTopicMessages: subscription error: %v", err)
			return // Exit on other errors too for simplicity
		}

		if msg.ReceivedFrom == myID {
			continue // Skip messages broadcast by self
		}

		var serverMsg clientpb.ServerMsg
		if err := proto.Unmarshal(msg.Data, &serverMsg); err != nil {
			log.Printf("[gossip Ctrl] processControlTopicMessages: failed to unmarshal ServerMsg from %s: %v", msg.ReceivedFrom, err)
			continue
		}

		switch payload := serverMsg.GetPayload().(type) {
		case *clientpb.ServerMsg_QueueUpdate:
			handleGossipQueueUpdate(payload.QueueUpdate, msg.ReceivedFrom, srv)
		case *clientpb.ServerMsg_RoleDefinitionsUpdate:
			handleGossipRoleDefinitionsUpdate(payload.RoleDefinitionsUpdate, msg.ReceivedFrom, srv)
		case *clientpb.ServerMsg_PeerRoleAssignment:
			handleGossipPeerRoleAssignment(payload.PeerRoleAssignment, msg.ReceivedFrom, srv)
		case *clientpb.ServerMsg_AllPeerRoleAssignments:
			handleGossipAllPeerRoleAssignments(payload.AllPeerRoleAssignments, msg.ReceivedFrom, srv)
		case *clientpb.ServerMsg_PlaybackStateCmd:
			handleGossipPlaybackStateCmd(payload.PlaybackStateCmd, msg.ReceivedFrom, srv)
		default:
			log.Printf("[gossip Ctrl] processControlTopicMessages: received unhandled ServerMsg payload type %T from %s", payload, msg.ReceivedFrom)
		}
	}
}

// processChatTopicMessages handles incoming ChatMsg messages from the chatTopic.
func processChatTopicMessages(ctx context.Context, sub *pubsub.Subscription, srv *server) {
	myID := srv.node.ID()
	log.Println("[gossip Chat] processChatTopicMessages started for peer", myID)
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("[gossip Chat] context cancelled, exiting chat message processor.")
				return
			}
			log.Printf("[gossip Chat] subscription error: %v", err)
			return
		}

		if msg.ReceivedFrom == myID {
			continue // Skip messages broadcast by self
		}

		var serverMsg clientpb.ServerMsg
		if err := proto.Unmarshal(msg.Data, &serverMsg); err != nil {
			log.Printf("[gossip Chat] failed to unmarshal ServerMsg (expecting ChatMsg) from %s: %v", msg.ReceivedFrom, err)
			continue
		}

		if chatPayload, ok := serverMsg.GetPayload().(*clientpb.ServerMsg_ChatMsg); ok {
			log.Printf("[gossip ChatRecv] Received ChatMsg (ID: %s, Sender: %s, Text: '%.20s...', HLC: %d) from %s. Broadcasting to local hub.",
				chatPayload.ChatMsg.MessageId, chatPayload.ChatMsg.Sender, chatPayload.ChatMsg.Text, chatPayload.ChatMsg.HlcTs, msg.ReceivedFrom)
			// Forward the *entire* ServerMsg (which wraps ChatMsg) to local GUI clients
			srv.hub.Broadcast(&serverMsg)
		} else {
			log.Printf("[gossip Chat] received non-ChatMsg payload on chat topic from %s: %T", msg.ReceivedFrom, serverMsg.GetPayload())
		}
	}
}

func handleGossipQueueUpdate(update *p2ppb.QueueUpdate, from peer.ID, srv *server) {
	log.Printf("[gossip Ctrl] handleGossipQueueUpdate: received from %s (HLC: %d)", from, update.HlcTs)
	srv.queueCtrl.ApplyUpdate(update, srv.hub, srv.node, srv.roleManager)
}

func handleGossipRoleDefinitionsUpdate(update *p2ppb.RoleDefinitionsUpdate, from peer.ID, srv *server) {
	log.Printf("[gossip Ctrl] handleGossipRoleDefinitionsUpdate: received from %s (HLC: %d)", from, update.HlcTs)
	if err := srv.roleManager.ApplyDefinitionsUpdate(update); err != nil {
		log.Printf("[gossip Ctrl] Error applying RoleDefinitionsUpdate from %s: %v", from, err)
	} else {
		// Successfully applied. Notify local GUI clients about the change in definitions.
		log.Printf("[gossip Ctrl] Applied RoleDefinitionsUpdate from %s. Broadcasting to local hub.", from)
		serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: update}}
		srv.hub.Broadcast(serverMsg)
		// Note: Applying new definitions might change effective permissions for *many* peers.
		// A more advanced system might re-evaluate and broadcast individual PeerRoleAssignments
		// if effective permissions changed, or simply expect clients to re-calculate.
		// For now, also re-evaluate this node's runner state as its permissions might have changed.
		// This is a broad action; finer-grained reactions could be implemented.
		if srv.node != nil && srv.queueCtrl != nil {
			srv.node.ReactToQueueUpdate()
		}
	}
}

func handleGossipPeerRoleAssignment(assignment *p2ppb.PeerRoleAssignment, from peer.ID, srv *server) {
	log.Printf("[gossip Ctrl] handleGossipPeerRoleAssignment: for peer %s from %s (HLC: %d)", assignment.PeerId, from, assignment.HlcTs)
	targetPeerID, err := peer.Decode(assignment.PeerId)
	if err != nil {
		log.Printf("[gossip Ctrl] Invalid peer ID '%s' in PeerRoleAssignment from %s: %v", assignment.PeerId, from, err)
		return
	}
	changed, err := srv.roleManager.ApplyPeerAssignment(targetPeerID, assignment.AssignedRoleNames, assignment.HlcTs)
	if err != nil {
		log.Printf("[gossip Ctrl] Error applying PeerRoleAssignment for %s from %s: %v", targetPeerID, from, err)
	} else if changed {
		log.Printf("[gossip Ctrl] Applied PeerRoleAssignment for %s from %s.", targetPeerID, from)
		// Notify local GUI about this specific assignment change
		serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: assignment}}
		srv.hub.Broadcast(serverMsg)
		if targetPeerID == srv.node.ID() && srv.node != nil && srv.queueCtrl != nil {
			log.Printf("[gossip Ctrl] Own roles changed due to PeerRoleAssignment from %s. Re-evaluating runner.", from)
			srv.node.ReactToQueueUpdate()
		}
	}
}

func handleGossipAllPeerRoleAssignments(snapshot *p2ppb.AllPeerRoleAssignments, from peer.ID, srv *server) {
	log.Printf("[gossip Ctrl] handleGossipAllPeerRoleAssignments: received from %s (HLC: %d)", from, snapshot.HlcTs)
	if err := srv.roleManager.ApplyAllAssignmentsSnapshot(snapshot); err != nil {
		log.Printf("[gossip Ctrl] Error applying AllPeerRoleAssignments from %s: %v", from, err)
	} else {
		log.Printf("[gossip Ctrl] Applied AllPeerRoleAssignments from %s. Broadcasting to local hub and re-evaluating runner.", from)
		serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_AllPeerRoleAssignments{AllPeerRoleAssignments: snapshot}}
		srv.hub.Broadcast(serverMsg)
		if srv.node != nil && srv.queueCtrl != nil {
			srv.node.ReactToQueueUpdate()
		}
	}
}

// handleGossipPlaybackStateCmd forwards a SetPlaybackStateCmd received via GossipSub to the local GUI via the Hub.
//
//	func handleGossipPlaybackStateCmd(cmd *p2ppb.SetPlaybackStateCmd, from peer.ID, hub *p2p.Hub) {
//		log.Printf("[gossip PlaybackRecv] Received SetPlaybackStateCmd (Play:%v Pos:%.2f Speed:%.2f HLC:%d) from %s. Broadcasting to local hub.", cmd.TargetIsPlaying, cmd.TargetTimePos, cmd.TargetSpeed, cmd.HlcTs, from)
func handleGossipPlaybackStateCmd(cmd *p2ppb.SetPlaybackStateCmd, from peer.ID, srv *server) {
	log.Printf("[gossip PlaybackRecv] Received SetPlaybackStateCmd (Play:%v Pos:%.2f Speed:%.2f HLC:%d StreamSeq: %d) from %s.",
		cmd.TargetIsPlaying, cmd.TargetTimePos, cmd.TargetSpeed, cmd.HlcTs, cmd.StreamSequenceId, from)

	// Update QueueController's state *before* broadcasting to local GUI.
	srv.queueCtrl.UpdateLastKnownPlaybackState(cmd)

	serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaybackStateCmd{PlaybackStateCmd: cmd}} // Updated ServerMsg field

	srv.hub.Broadcast(serverMsg) // Send to the connected GUI client(s)
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
		case event, ok := <-eventChan:
			if !ok {
				log.Println("[SegmentProcessor] Segment event channel closed. Stopping.")
				return
			}
			// This node's ffmpeg produced a segment and it landed in the RingBuffer.
			// Decision Q2: "Only owner peer runs ffmpeg locally" implies this node IS the streamer.
			// We still do a sanity check against roles and queue state.
			isDesignatedStreamer := false
			headItem, headOk := s.queueCtrl.Q().Head()
			if headOk {
				localPeerID := s.node.ID()
				permissions := s.roleManager.GetPermissionsForPeer(localPeerID)

				// Check if the current node is the one designated to store and stream THIS item
				// AND if the sequence number from the event matches what's expected for this stream.
				// The latter check is complex if seqBase is not perfectly synced, so for now,
				// primarily rely on StoredBy and PermStream.
				if headItem.StoredBy == localPeerID && permissions.Has(roles.PermStream) {
					// Optional: More detailed check if event.Seq aligns with expected sequence for headItem
					// and if headItem.FilePath matches the one EncoderRunner is using.
					isDesignatedStreamer = true
				}
			}

			if !isDesignatedStreamer {
				log.Printf("[SegmentProcessor] Segment %d from local RingBuffer, but node (%s) is not designated streamer for current head. NOT publishing.", event.Seq, s.node.ID())
				continue
			}

			segmentData := rb.Get(event.Seq) // Retrieve segment data from RingBuffer
			if segmentData == nil {
				log.Printf("[SegmentProcessor] Segment %d not found in RingBuffer after event. Skipping publish.", event.Seq)
				continue
			}

			videoMsg := &p2ppb.VideoSegmentGossip{ // Use the p2ppb alias
				SequenceNumber:        event.Seq,
				Data:                  segmentData,
				ActualDurationSeconds: rb.GetSegmentActualDuration(event.Seq),
			}
			marshalledVideoMsg, err := proto.Marshal(videoMsg)
			if err != nil {
				log.Printf("[gossip VideoPublish] Failed to marshal VideoSegmentGossip for seq %d: %v", event.Seq, err)
				continue
			}

			if videoTopic := s.node.VideoTopic(); videoTopic != nil {
				if err := videoTopic.Publish(ctx, marshalledVideoMsg); err != nil {
					if ctx.Err() != nil {
						log.Printf("[gossip VideoPublish] Context error publishing video segment %d: %v", event.Seq, ctx.Err())
						return // Exit if context is done
					}
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
		rb.WriteAt(videoSegment.SequenceNumber, videoSegment.Data, 0, videoSegment.ActualDurationSeconds)
		rb.SetPublishEvents(true) // Re-enable for segments from local ffmpeg

		log.Printf("[gossip VideoReceive] Received and stored video segment %d (%d bytes) from %s.", videoSegment.SequenceNumber, len(videoSegment.Data), msg.ReceivedFrom)
		s.node.AddBytesDown(uint64(len(msg.Data))) // Track incoming gossip traffic
	}
}

func main() {
	// flags
	var grpcPort int
	flag.IntVar(&grpcPort, "grpc-port", 0, "gRPC listen port")
	var initialRolesStr string
	flag.StringVar(&initialRolesStr, "roles", "Admin", "Initial roles for this node (comma-separated, e.g., 'Streamer,Viewer')")
	flag.Parse()

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx() // Ensure cancellation on exit

	// 1) Start HTTP mini-HLS on a random port
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("HTTP listen failed: %v", err)
	}
	hlsPort := uint32(httpLn.Addr().(*net.TCPAddr).Port)
	log.Printf("HLS endpoint listening on %s\n", httpLn.Addr().String())

	// --- mini‑HLS in‑RAM buffer ---
	rb := media.NewRingBuffer(120) // 120 s window
	plH, segH, triggerDiscontinuity := media.Handler(rb)

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/stream.m3u8", plH)
	httpMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/seg_") && strings.HasSuffix(r.URL.Path, ".ts"):
			segH(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	httpServer := &http.Server{Handler: httpMux}

	// Goroutine to start the HLS server
	go func() {
		log.Println("Starting HLS server...")
		if err := httpServer.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HLS server error: %v", err)
		}
		log.Println("HLS server goroutine finished.") // Add log
	}()

	// 2) Start gRPC server
	if grpcPort == 0 { // If port 0 was passed, pick a free one
		tempLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalf("Failed to find free port for gRPC: %v", err)
		}
		grpcPort = tempLn.Addr().(*net.TCPAddr).Port
		tempLn.Close() // Close it, we'll listen on it properly soon
	}

	grpcLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", grpcPort))
	if err != nil {
		log.Fatalf("gRPC listen on port %d failed: %v", grpcPort, err)
	}
	log.Printf("gRPC server listening on 127.0.0.1:%d\n", grpcPort)

	grpcServer := grpc.NewServer()

	// 3) libp2p + GossipSub
	lhost, _ := buildHost(ctx) // DHT not directly used by server instance for now
	node := p2p.NewNode(lhost, hlsPort, rb)
	hub := p2p.NewHub()
	queueCtrl := p2p.NewQueueController(triggerDiscontinuity)
	roleManager := roles.NewRoleManager()

	// Assign default roles (e.g., "Admin" or "Streamer,Viewer") to the service's own node ID
	// This allows the service itself (when acting as sender) to have permissions.
	// For testing self-role changes via GUI, it needs PermManageUserRoles if target is self.
	// Giving Admin role to self for now for full testing capability.
	parsedInitialRoles, err := roleManager.ParseRoleNames(initialRolesStr)
	if err != nil {
		log.Fatalf("Invalid initial roles string '%s': %v", initialRolesStr, err)
	}
	if len(parsedInitialRoles) > 0 {
		roleNames := make([]string, len(parsedInitialRoles))
		for i, r := range parsedInitialRoles {
			roleNames[i] = r.Name // Use the (potentially normalized) name from parsed Role struct
		}
		_, err = roleManager.SetPeerRoles(node.ID(), roleNames)
		if err != nil {
			log.Fatalf("Failed to set initial self-roles for service node: %v", err)
		}
		log.Printf("Service node %s initialized with roles: %v in RoleManager.", node.ID(), roleNames)
	} else {
		log.Printf("Service node %s initialized with no specific roles from command line.", node.ID())
	}
	node.SetDependencies(queueCtrl, roleManager, hub) // Inject dependencies into Node

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

	queueCtrl.SetDependencies(node, hub, roleManager, ctrlTopic)

	node.AttachPubSub(videoTopic, chatTopic, ctrlTopic)

	// Subscribe to the control topic for various ServerMsg types
	subCtrl, err := ctrlTopic.Subscribe()
	if err != nil {
		log.Fatalf("ctrl topic subscribe: %v", err)
	}

	if _, err := lhost.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged)); err != nil {
		log.Fatalf("event‑bus subscribe failed: %v", err)
	}

	// clientpb.RegisterP2PTClientServer(grpcServer, &server{
	// Define the server instance that holds all dependencies
	srvInstance := &server{
		hlsPort:                 hlsPort,
		hub:                     hub,
		queueCtrl:               queueCtrl,
		node:                    node,
		roleManager:             roleManager,
		ctrlTopic:               ctrlTopic,
		chatTopic:               chatTopic,
		triggerHLSDircontinuity: triggerDiscontinuity,
	}
	clientpb.RegisterP2PTClientServer(grpcServer, srvInstance)

	// Register IngestHandler with the fully initialized node (which can provide durations)
	httpMux.HandleFunc("/ingest/", media.IngestHandler(rb, srvInstance.node))

	subVideo, err := node.VideoTopic().Subscribe()
	if err != nil {
		log.Fatalf("video topic subscribe for receiving failed: %v", err)
	}

	// Start background processors
	go processControlTopicMessages(ctx, subCtrl, srvInstance)

	// Chat topic subscription
	subChat, err := chatTopic.Subscribe()
	if err != nil {
		log.Fatalf("Chat topic subscribe failed: %v", err)
	}
	go processChatTopicMessages(ctx, subChat, srvInstance) // New goroutine for chat

	// Video topic subscription (for receiving segments)
	go srvInstance.processVideoSegmentMessages(ctx, subVideo, rb)

	// Segment event processor (for publishing segments from local encoder)
	go srvInstance.processSegmentEvents(ctx, rb)

	// fan‑out StreamStatus to every connected client
	statusCh := media.StartStatusTicker(rb, 5*time.Second) // Assuming this returns chan *p2ppb.StreamStatus
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case st, ok := <-statusCh:
				if !ok {
					return
				} // Channel closed
				srvInstance.hub.Broadcast(&clientpb.ServerMsg{
					Payload: &clientpb.ServerMsg_StreamStatus{StreamStatus: st},
				})
			}
		}
	}()

	// Libp2p connection event logger
	connSub, _ := lhost.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	go func() {
		defer connSub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-connSub.Out():
				if !ok {
					return
				}
				e := ev.(event.EvtPeerConnectednessChanged)
				log.Printf("[conn] Peer %s: %s", e.Peer, e.Connectedness)
			}
		}
	}()

	// Goroutine to start the gRPC server
	go func() {
		log.Printf("gRPC server listening on %s\n", grpcLn.Addr().String())
		if err := grpcServer.Serve(grpcLn); err != nil && err.Error() != "grpc: the server has been stopped" {
			log.Printf("gRPC serve failed: %v", err) // Log as non-fatal if already stopping
		}
	}()

	// Trigger initial ReactToQueueUpdate to start encoder if applicable
	// Needs to be after all dependencies are set up.
	// A small delay might be useful if there's any race with topic subscriptions,
	// though usually not necessary for local logic.
	time.AfterFunc(1*time.Second, func() {
		log.Println("Triggering initial ReactToQueueUpdate.")
		srvInstance.node.ReactToQueueUpdate()
	})

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

	// Shutdown HTTP server
	log.Println("Attempting HTTP server Shutdown()...")
	shutdownCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer httpCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	} else {
		log.Println("HTTP server shutdown complete.")
	}

	if err := lhost.Close(); err != nil {
		log.Printf("Error closing libp2p host: %v", err)
	}
	log.Println("Libp2p host closed.")
	log.Println("Peer service exited gracefully.")
}
