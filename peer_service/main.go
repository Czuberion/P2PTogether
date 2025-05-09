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
	pbDefs := make([]*p2ppb.RoleDefinition, 0, len(defs))
	for _, d := range defs {
		pbDefs = append(pbDefs, &p2ppb.RoleDefinition{Name: d.Name, Permissions: uint32(d.Permissions)})
	}
	defUpdate := &p2ppb.RoleDefinitionsUpdate{Definitions: pbDefs /* Add HLC TS */}
	_ = stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_RoleDefinitionsUpdate{RoleDefinitionsUpdate: defUpdate}}) // Error handling omitted for brevity

	// 2. Send All Peer Assignments (TODO: Need RoleManager method to get all assignments)
	// assignmentsSnapshot := s.roleManager.GetAllAssignments() // Assuming this method exists
	// allAssignmentsMsg := &p2ppb.AllPeerRoleAssignments{...} // Populate this
	// _ = stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_AllPeerRoleAssignments{AllPeerRoleAssignments: allAssignmentsMsg}})

	// 3. Send Current Queue State
	queueSnapshot := s.queueCtrl.Snapshot() // Assuming Snapshot() is public or accessible
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

		switch cmd := clientMsg.Payload.(type) {
		case *clientpb.ClientMsg_QueueCmd:

			// Pass context from stream for cancellation propagation if Publish blocks.
			// Pass s.node and s.ctrlTopic for Handle to use.
			// if err := s.queueCtrl.Handle(stream.Context(), x.QueueCmd, pid, perms, s.hub, s.node, s.ctrlTopic); err != nil {
			// 	log.Printf("Error handling QueueCmd from %s: %v", pid.String(), err)

			// Pass the node's ID as the 'sender' for commands originating from the local gRPC client.

			// Permission check now happens inside Handle using RoleManager
			if err := s.queueCtrl.Handle(stream.Context(), cmd.QueueCmd, s.node.ID(), s.roleManager, s.hub, s.node, s.ctrlTopic); err != nil {
				log.Printf("Error handling QueueCmd from local client (node %s): %v", nodeIDStr, err)
				// Decide if the error is fatal for this stream
				// return err // Example: return error to close stream on failure
			}
		default:
			log.Printf("Received unhandled message type from %s", nodeIDStr)
		}
	}
}

// Handle incoming SetPeerRolesCmd
func (s *server) handleSetPeerRolesCmd(ctx context.Context, cmd *p2ppb.SetPeerRolesCmd, senderID peer.ID) {
	// 1. Check if sender has permission to manage roles
	senderPerms := s.roleManager.GetPermissionsForPeer(senderID)
	if !senderPerms.Has(roles.PermManageUserRoles) {
		log.Printf("Permission denied: Peer %s lacks PermManageUserRoles to set roles for %s", senderID, cmd.TargetPeerId)
		// Optionally send an error back to the sender via gRPC stream?
		return
	}

	// 2. Decode target peer ID and call RoleManager
	targetPeerID, err := peer.Decode(cmd.TargetPeerId)
	if err != nil {
		log.Printf("Failed to decode target peer ID '%s': %v", cmd.TargetPeerId, err)
		return // Handle error appropriately
	}

	err = s.roleManager.SetPeerRoles(targetPeerID, cmd.AssignedRoleNames)
	// ... (Error handling for SetPeerRoles) ...
	// SetPeerRoles should trigger broadcasts via Hub/Topic if successful
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

// pumpQueueUpdates handles incoming QueueUpdate messages from GossipSub.
func pumpQueueUpdates(ctx context.Context, sub *pubsub.Subscription, qc *p2p.QueueController, myID peer.ID, localHub *p2p.Hub, processingNode *p2p.Node, rm *roles.RoleManager) {
	log.Println("[gossip] pumpQueueUpdates started for peer", myID)
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			log.Printf("[gossip] pumpQueueUpdates: subscription error: %v", err)
			if ctx.Err() != nil {
				log.Println("[gossip] pumpQueueUpdates: context cancelled, exiting.")
				return
			}
			return // Exit on other errors too for simplicity
		}

		if msg.ReceivedFrom == myID {
			continue // Skip messages broadcast by self
		}

		var serverMsg clientpb.ServerMsg
		if err := proto.Unmarshal(msg.Data, &serverMsg); err != nil {
			log.Printf("[gossip] pumpQueueUpdates: failed to unmarshal ServerMsg from %s: %v", msg.ReceivedFrom, err)
			continue
		}

		switch payload := serverMsg.GetPayload().(type) {
		case *clientpb.ServerMsg_QueueUpdate:
			log.Printf("[gossip] pumpQueueUpdates: received QueueUpdate from %s", msg.ReceivedFrom)
			qc.ApplyUpdate(payload.QueueUpdate, localHub, processingNode, rm) // Pass rm
			// TODO: Handle other message types like RoleDefinitionsUpdate, PeerRoleAssignment
			// log.Printf("[gossip] pumpQueueUpdates: received ServerMsg from %s is not QueueUpdate. Type: %T", msg.ReceivedFrom, serverMsg.GetPayload())
		}
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
	go pumpQueueUpdates(ctx, subCtrl, queueCtrl, lhost.ID(), hub, node, roleManager)

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

	clientpb.RegisterP2PTClientServer(grpcServer, &server{
		hlsPort:     hlsPort,
		hub:         hub,
		queueCtrl:   queueCtrl,
		node:        node,
		roleManager: roleManager,
		ctrlTopic:   ctrlTopic,
	})

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
