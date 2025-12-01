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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"peer_service/internal/common"
	"peer_service/internal/media"
	"peer_service/internal/p2p"
	"peer_service/internal/roles"
	"peer_service/internal/session"
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

	// Session management
	sessionManager *session.SessionManager

	// Role Management
	roleManager *roles.RoleManager

	// GossipSub control topic
	ctrlTopic *pubsub.Topic

	// GossipSub chat topic
	chatTopic *pubsub.Topic

	// DHT for peer discovery and content routing
	dht *dht.IpfsDHT

	// routingDiscovery discovery.Discovery // Using the interface type
	routingDiscovery *drouting.RoutingDiscovery // Use the concrete type

	triggerHLSDircontinuity func()
}

func (s *server) GetServiceInfo(ctx context.Context, _ *emptypb.Empty) (*clientpb.ServiceInfo, error) {
	return &clientpb.ServiceInfo{HlsPort: s.hlsPort}, nil
}

// Send initial state (role definitions, all assignments, current queue) to a newly connected client.
func (s *server) sendInitialState(stream clientpb.P2PTClient_ControlStreamServer) {
	// 1. Send Local Peer Identity first
	var localUsername string
	currentSessID := s.node.GetCurrentSessionID()
	if currentSessID != "" && currentSessID != "default" { // Only fetch username if in a real session
		currentSession, sessExists := s.sessionManager.GetSession(currentSessID)
		if sessExists {
			if userInfo, userExists := currentSession.Members[s.node.ID()]; userExists {
				localUsername = userInfo.Username
			}
		}
	}

	identityMsg := &p2ppb.LocalPeerIdentity{PeerId: s.node.ID().String()}
	identityMsg.Username = &localUsername // Assign optional username
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
		} else if cmdPl, ok := clientMsg.Payload.(*clientpb.ClientMsg_ChatCmd); ok {
			hlc = cmdPl.ChatCmd.HlcTs
			cmdTypeString = fmt.Sprintf("ChatCmd (Text: '%.20s...')", cmdPl.ChatCmd.Text)
		} else {
			cmdTypeString = fmt.Sprintf("Unknown type %T", clientMsg.Payload)
		}
		log.Printf("[gRPC Recv %s] %s, HLC: %d", nodeIDStr, cmdTypeString, hlc)

		switch cmd := clientMsg.Payload.(type) {
		case *clientpb.ClientMsg_CreateSessionRequest:
			req := cmd.CreateSessionRequest
			log.Printf("[gRPC Recv %s] CreateSessionRequest: Name='%s', User='%s'", nodeIDStr, req.GetSessionName(), req.GetUsername())

			createdSession, err := s.sessionManager.CreateSession(senderID, req.GetUsername(), req.GetSessionName())
			if err != nil {
				log.Printf("Error creating session for %s: %v", nodeIDStr, err)
				resp := &p2ppb.CreateSessionResponse{
					ErrorMessage: proto.String(fmt.Sprintf("Failed to create session: %v", err)),
				}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_CreateSessionResponse{CreateSessionResponse: resp}}); errSend != nil {
					log.Printf("Error sending CreateSession error response: %v", errSend)
				}
				continue // Next message from client
			}

			log.Printf("Session %s created by %s. Invite Code: %s", createdSession.ID, nodeIDStr, createdSession.InviteCode)

			// Channel to signal completion of the advertisement verification
			verificationCompleteChan := make(chan struct{})

			// Advertise the session ID as a rendez-vous string with enhanced DHT propagation
			go func(sessID string, verificationDone chan<- struct{}) {
				// Use the stream's context for advertisement. When the stream (client connection) ends,
				// the context will be cancelled, and dutil.Advertise will stop.
				// This ties the advertisement lifecycle to the creator's connection.
				sessionAdvCtx := stream.Context()

				log.Printf("Starting persistent DHT advertisement for session %s using stream context", sessID)
				log.Printf("DHT routing table size: %d", s.dht.RoutingTable().Size())
				if s.dht.RoutingTable().Size() > 0 {
					dhtPeers := s.dht.RoutingTable().ListPeers()
					peerCount := len(dhtPeers)
					if peerCount > 5 {
						peerCount = 5
					}
					log.Printf("Sample DHT peers (first %d): %v", peerCount, dhtPeers[:peerCount])
				}

				// Initial and persistent advertisement.
				// dutil.Advertise will run in the background, periodically re-advertising
				// until sessionAdvCtx is cancelled.
				log.Printf("Initiating persistent advertisement for session %s via DHT", sessID)
				log.Printf("DHT routing table size before advertisement: %d", s.dht.RoutingTable().Size())
				log.Printf("Our peer ID: %s", s.node.ID())
				dutil.Advertise(sessionAdvCtx, s.routingDiscovery, sessID)
				log.Printf("Persistent advertisement process started for session %s", sessID)

				// Verify our own advertisement is discoverable after a delay.
				go func() {
					defer func() {
						log.Printf("Verification goroutine for session %s signaling completion.", sessID)
						verificationDone <- struct{}{}
						close(verificationDone) // Close channel to signal completion
					}()

					// Allow more time for DHT propagation in WAN scenarios before verification.
					time.Sleep(15 * time.Second)
					verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 30*time.Second) // Longer timeout for finding peers
					defer verifyCancel()

					log.Printf("Verifying advertisement propagation for session %s", sessID)
					peerChan, err := s.routingDiscovery.FindPeers(verifyCtx, sessID)
					if err != nil {
						log.Printf("Advertisement verification FindPeers call failed for session %s: %v", sessID, err)
						// The main dutil.Advertise(sessionAdvCtx, ...) should still be running.
						// Signal completion via defer.
						return
					}

					foundSelf := false
					peerCount := 0
					for pInfo := range peerChan { // Loop will stop when channel is closed (e.g. by timeout)
						if pInfo.ID == "" { // Skip empty AddrInfo
							continue
						}
						peerCount++
						if pInfo.ID == s.node.ID() {
							foundSelf = true
							// No need to break, let's see how many peers are found in total for logging.
						}
					}

					if foundSelf {
						log.Printf("Advertisement verification SUCCESS: Session %s self-discovery confirmed. Found %d peers (including self).", sessID, peerCount)
					} else {
						log.Printf("Advertisement verification WARNING: Session %s self-discovery failed. Found %d other peers. The session might not be reliably discoverable.", sessID, peerCount)
						// The main advertisement (tied to sessionAdvCtx) should still be active.
						// This fallback advertisement uses context.Background(), making it independent of the stream.
						// This is a strong measure if the primary advertisement mechanism faces issues.
						log.Printf("Attempting a fallback persistent re-advertisement for session %s due to verification failure", sessID)
						dutil.Advertise(context.Background(), s.routingDiscovery, sessID)
					}
					// Signal completion via defer.
				}()
			}(createdSession.ID, verificationCompleteChan)

			// Admin node joins the topics for the session it just created
			if err := s.node.JoinSessionTopics(createdSession.ID); err != nil {
				log.Printf("Admin node %s failed to join session topics for %s: %v", senderID, createdSession.ID, err)
			} else {
				// Update server's view of current topics
				s.ctrlTopic = s.node.CtrlTopic()
				s.chatTopic = s.node.ChatTopic()
			}

			// Send an updated LocalPeerIdentity for the admin now that they are in a session with a username
			s.sendInitialState(stream) // Or a more targeted LocalPeerIdentity update
			log.Printf("TODO: Persist session %s for Admin %s (SR-SM-2)", createdSession.ID, nodeIDStr)

			// Wait for advertisement verification to complete before sending response
			log.Printf("Waiting for advertisement verification to complete for session %s before sending response to client...", createdSession.ID)
			<-verificationCompleteChan
			log.Printf("Advertisement verification completed for session %s. Proceeding to send CreateSessionResponse.", createdSession.ID)

			createResp := &p2ppb.CreateSessionResponse{SessionId: createdSession.ID, InviteCode: createdSession.InviteCode}
			if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_CreateSessionResponse{CreateSessionResponse: createResp}}); errSend != nil {
				log.Printf("Error sending CreateSessionResponse: %v", errSend)
			}
			continue // Successfully handled, move to next client message

		case *clientpb.ClientMsg_JoinSessionRequest:
			req := cmd.JoinSessionRequest
			sessionIDToJoin := req.InviteCode
			joiningUsername := ""
			if req.Username != nil {
				joiningUsername = *req.Username
			}

			log.Printf("Attempting DHT FindPeers for session: %s with enhanced discovery timing", sessionIDToJoin)
			log.Printf("Our peer ID: %s", s.node.ID())
			log.Printf("Our external addresses: %v", s.node.Addrs())

			// Check DHT connectivity before discovery
			connected := s.dht.RoutingTable().Size()
			log.Printf("DHT routing table size: %d peers", connected)

			// Log some of the DHT peers we're connected to
			if connected > 0 {
				dhtPeers := s.dht.RoutingTable().ListPeers()
				peerCount := len(dhtPeers)
				if peerCount > 5 {
					peerCount = 5
				}
				log.Printf("Sample DHT peers (first %d): %v", peerCount, dhtPeers[:peerCount])
			}

			var adminInfo *peer.AddrInfo
			var foundAdmin bool

			// Enhanced DHT discovery with proper timing for provider record propagation
			maxRetries := 4 // Increased retry count
			for attempt := 1; attempt <= maxRetries; attempt++ {
				log.Printf("DHT discovery attempt %d/%d for session %s", attempt, maxRetries, sessionIDToJoin)

				// Progressive timeout increase to allow for DHT propagation
				baseTimeout := 8 + (attempt * 3) // 11s, 14s, 17s, 20s
				timeoutDuration := time.Duration(baseTimeout) * time.Second

				// For early attempts, add additional delay to account for advertisement propagation
				if attempt == 1 {
					log.Printf("First discovery attempt - waiting extra time for DHT propagation...")
					time.Sleep(5 * time.Second) // Wait for provider record propagation
				} else if attempt == 2 {
					log.Printf("Second discovery attempt - allowing more propagation time...")
					time.Sleep(3 * time.Second) // Additional propagation time
				}

				findPeersCtx, findPeersCancel := context.WithTimeout(stream.Context(), timeoutDuration)

				peerChan, err := s.routingDiscovery.FindPeers(findPeersCtx, sessionIDToJoin)
				if err != nil {
					log.Printf("DHT FindPeers attempt %d failed: %v", attempt, err)
					findPeersCancel()
					continue
				}

				// Process peers found for this session
				peerCount := 0
				for pInfo := range peerChan {
					peerCount++
					log.Printf("Discovery attempt %d: Found peer %d: %s with addrs %v", attempt, peerCount, pInfo.ID, pInfo.Addrs)

					if pInfo.ID == s.node.ID() {
						log.Printf("Found self (%s) as provider. Skipping.", s.node.ID())
						continue
					}
					log.Printf("Found potential admin for session %s: %s", sessionIDToJoin, pInfo.ID)
					adminInfo = &pInfo
					foundAdmin = true
					break
				}

				log.Printf("Discovery attempt %d completed. Peers found: %d", attempt, peerCount)
				findPeersCancel()

				if foundAdmin {
					log.Printf("Successfully found admin after attempt %d", attempt)
					break // Exit retry loop
				}

				if attempt < maxRetries {
					// Exponential backoff with additional DHT propagation considerations
					backoffDelay := time.Duration(attempt*3+2) * time.Second // 5s, 8s, 11s
					log.Printf("No admin found in attempt %d, waiting %v before retry (allowing DHT propagation)", attempt, backoffDelay)
					time.Sleep(backoffDelay)
				}
			}

			if !foundAdmin {
				log.Printf("DHT discovery failed for session %s. Attempting FALLBACK STRATEGIES...", sessionIDToJoin)

				// FALLBACK STRATEGY 1: Enhanced Peer Exchange via Connected Peers
				log.Printf("=== FALLBACK: ENHANCED PEER EXCHANGE STRATEGY ===")
				connectedPeers := s.node.Network().Peers()
				log.Printf("Total connected peers: %d. Filtering for application peers...", len(connectedPeers))

				// Filter connected peers to find those that might be application peers
				// Application peers should have protocol support or be directly reachable
				appPeers := make([]peer.ID, 0)
				for _, connectedPeer := range connectedPeers {
					// Check if peer supports our protocol or has been seen with our protocol
					protocols, err := s.node.Peerstore().GetProtocols(connectedPeer)
					if err == nil {
						for _, proto := range protocols {
							if strings.Contains(string(proto), "p2ptogether") {
								appPeers = append(appPeers, connectedPeer)
								break
							}
						}
					}
				}

				log.Printf("Found %d potential application peers out of %d total peers", len(appPeers), len(connectedPeers))

				// Try peer exchange with application peers first
				peersToTry := appPeers
				if len(peersToTry) == 0 {
					// Fallback to trying first 5 connected peers if no app peers found
					maxTryCount := 5
					if len(connectedPeers) < maxTryCount {
						maxTryCount = len(connectedPeers)
					}
					peersToTry = connectedPeers[:maxTryCount]
					log.Printf("No application peers found, trying first %d connected peers", len(peersToTry))
				}

				for i, connectedPeer := range peersToTry {
					if i >= 10 { // Still limit to avoid spam
						break
					}
					log.Printf("Peer exchange attempt %d: Asking peer %s about session %s", i+1, connectedPeer, sessionIDToJoin)

					// Try to connect and ask this peer if they know about the session
					exchangeCtx, exchangeCancel := context.WithTimeout(stream.Context(), 3*time.Second) // Shorter timeout

					// Simple approach: Try to open a temporary stream to see if this peer might be the admin
					tempStream, err := s.node.NewStream(exchangeCtx, connectedPeer, "/p2ptogether/auth/1")
					if err != nil {
						log.Printf("Peer exchange: Peer %s doesn't support /auth/1 protocol: %v", connectedPeer, err)
						exchangeCancel()
						continue
					}

					// Send a test auth request to see if this peer manages our target session
					testAuth := &p2ppb.AuthRequest{
						SessionId: sessionIDToJoin,
						Username:  "discovery-test", // Special username for discovery
					}
					testAuthBytes, err := proto.Marshal(testAuth)
					if err != nil {
						tempStream.Close()
						exchangeCancel()
						continue
					}

					_, err = tempStream.Write(testAuthBytes)
					if err != nil {
						log.Printf("Peer exchange: Failed to write test auth to peer %s: %v", connectedPeer, err)
						tempStream.Close()
						exchangeCancel()
						continue
					}

					// Try to read response with shorter timeout
					respBuf := make([]byte, 1024)
					tempStream.SetReadDeadline(time.Now().Add(2 * time.Second))
					n, err := tempStream.Read(respBuf)
					tempStream.Close()
					exchangeCancel()

					if err != nil {
						log.Printf("Peer exchange: No response from peer %s: %v", connectedPeer, err)
						continue
					}

					var authResp p2ppb.JoinSessionResponse
					if err := proto.Unmarshal(respBuf[:n], &authResp); err != nil {
						log.Printf("Peer exchange: Invalid response from peer %s: %v", connectedPeer, err)
						continue
					}

					// Check if this peer acknowledged the session (even with an error)
					if authResp.ErrorMessage != nil && !strings.Contains(*authResp.ErrorMessage, "Session not found") {
						log.Printf("Peer exchange: SUCCESS! Found session admin at %s", connectedPeer)
						// This peer knows about the session, use it as adminInfo
						peerInfo := s.node.Peerstore().PeerInfo(connectedPeer)
						adminInfo = &peerInfo
						foundAdmin = true
						break
					} else {
						log.Printf("Peer exchange: Peer %s doesn't manage session %s", connectedPeer, sessionIDToJoin)
					}
				}

				if !foundAdmin {
					// FALLBACK STRATEGY 2: Direct Local Network Discovery via mDNS
					log.Printf("=== FALLBACK: LOCAL NETWORK DISCOVERY (mDNS) ===")
					log.Printf("Attempting to discover session admin via local network...")

					// For local network environments, Alice and Bob might be on the same LAN
					// but in different DHT partitions. Try mDNS approach.
					// Since we're already connected to many peers, check if any have our target session

					// Look through our connected peers for any that might have recently advertised sessions
					log.Printf("Checking recently connected peers for session patterns...")

					// Get list of peers we're connected to via libp2p network
					connectedViaNetwork := s.node.Network().Conns()
					log.Printf("Active network connections: %d", len(connectedViaNetwork))

					// Try to find the session admin by checking peer IDs we've seen recently
					// This is a simple approach for local network discovery
					for _, conn := range connectedViaNetwork {
						if conn.RemotePeer() == s.node.ID() {
							continue // Skip self
						}

						// Try to establish if this peer might be running our application
						log.Printf("Testing connection to peer %s for application presence", conn.RemotePeer())

						testCtx, testCancel := context.WithTimeout(stream.Context(), 2*time.Second)
						testStream, err := s.node.NewStream(testCtx, conn.RemotePeer(), "/p2ptogether/auth/1")
						if err != nil {
							testCancel()
							continue // Not an application peer
						}

						// This peer supports our protocol, test for session
						testAuth := &p2ppb.AuthRequest{
							SessionId: sessionIDToJoin,
							Username:  "discovery-test",
						}
						testAuthBytes, _ := proto.Marshal(testAuth)
						testStream.Write(testAuthBytes)

						respBuf := make([]byte, 1024)
						testStream.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
						n, err := testStream.Read(respBuf)
						testStream.Close()
						testCancel()

						if err == nil && n > 0 {
							var authResp p2ppb.JoinSessionResponse
							if proto.Unmarshal(respBuf[:n], &authResp) == nil {
								if authResp.ErrorMessage == nil || !strings.Contains(*authResp.ErrorMessage, "Session not found") {
									log.Printf("Local discovery: SUCCESS! Found session admin at %s", conn.RemotePeer())
									peerInfo := s.node.Peerstore().PeerInfo(conn.RemotePeer())
									adminInfo = &peerInfo
									foundAdmin = true
									break
								}
							}
						}
					}
				}

				if !foundAdmin {
					// FALLBACK STRATEGY 3: Enhanced Bootstrap Peer Discovery
					log.Printf("=== FALLBACK: ENHANCED BOOTSTRAP DISCOVERY ===")
					log.Printf("Attempting to discover session via enhanced bootstrap methods...")

					// Try to re-bootstrap with additional peers and expand our DHT view
					log.Printf("Re-bootstrapping DHT to expand network view...")
					bootstrapCtx, bootstrapCancel := context.WithTimeout(stream.Context(), 15*time.Second) // Shorter timeout

					if err := s.dht.Bootstrap(bootstrapCtx); err != nil {
						log.Printf("Fallback bootstrap failed: %v", err)
					} else {
						log.Printf("Fallback bootstrap completed, DHT size now: %d", s.dht.RoutingTable().Size())

						// Try one more round of discovery after bootstrap with just the primary key
						log.Printf("Post-bootstrap discovery attempt for session %s", sessionIDToJoin)
						postBootstrapCtx, postBootstrapCancel := context.WithTimeout(stream.Context(), 10*time.Second) // Shorter timeout

						peerChan, err := s.routingDiscovery.FindPeers(postBootstrapCtx, sessionIDToJoin)
						if err != nil {
							log.Printf("Post-bootstrap discovery failed: %v", err)
						} else {
							for pInfo := range peerChan {
								if pInfo.ID != s.node.ID() {
									log.Printf("Post-bootstrap: Found session admin %s", pInfo.ID)
									adminInfo = &pInfo
									foundAdmin = true
									break
								}
							}
						}
						postBootstrapCancel()
					}
					bootstrapCancel()
				}

				if !foundAdmin {
					errMsg := fmt.Sprintf("No providers found for session %s on the DHT after exhaustive search (standard DHT + peer exchange + bootstrap).", sessionIDToJoin)
					log.Println(errMsg)
					resp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
					if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: resp}}); errSend != nil {
						log.Printf("Error sending JoinSession error response (no providers): %v", errSend)
					}
					continue
				} else {
					log.Printf("SUCCESS: Found session admin via fallback strategy: %s", adminInfo.ID)
				}
			}

			// ENHANCED CONNECTION ESTABLISHMENT WITH NAT TRAVERSAL
			log.Printf("=== ENHANCED CONNECTION ESTABLISHMENT ===")
			log.Printf("Attempting to connect to Admin %s for session %s", adminInfo.ID, sessionIDToJoin)
			log.Printf("Admin multiaddresses: %v", adminInfo.Addrs)

			// Check our current connectivity status
			log.Printf("Our host connectivity: %s", s.node.Network().Connectedness(adminInfo.ID))
			log.Printf("Our external addresses: %v", s.node.Addrs())

			var connectionSuccessful bool
			var connectionMethod string

			// Strategy 1: Direct connection with multiple attempts
			for attempt := 1; attempt <= 3 && !connectionSuccessful; attempt++ {
				log.Printf("Direct connection attempt %d/3 to Admin %s", attempt, adminInfo.ID)

				connectCtx, connectCancel := context.WithTimeout(stream.Context(), time.Duration(10+attempt*5)*time.Second)

				if err := s.node.Connect(connectCtx, *adminInfo); err != nil {
					log.Printf("Direct connection attempt %d failed: %v", attempt, err)
					connectCancel()

					// Wait before retry
					if attempt < 3 {
						time.Sleep(time.Duration(attempt) * time.Second)
					}
				} else {
					log.Printf("Direct connection attempt %d SUCCESSFUL!", attempt)
					connectionSuccessful = true
					connectionMethod = "direct"
					connectCancel()
					break
				}
			}

			// Strategy 2: If direct connection failed, try to use relay
			if !connectionSuccessful {
				log.Printf("=== ATTEMPTING RELAY CONNECTION ===")

				// First, let's identify potential relay peers
				connectedPeers := s.node.Network().Peers()
				log.Printf("Connected peers available for relay: %d", len(connectedPeers))

				relayAttempts := 0
				maxRelayAttempts := 2

				for _, relayPeer := range connectedPeers {
					if relayPeer == s.node.ID() || relayPeer == adminInfo.ID {
						continue // Skip self and target
					}

					if relayAttempts >= maxRelayAttempts {
						break
					}

					relayAttempts++
					log.Printf("Relay attempt %d: Trying to connect to %s via relay %s", relayAttempts, adminInfo.ID, relayPeer)

					// Create a circuit relay address to the target through this relay
					relayAddr := fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", relayPeer, adminInfo.ID)
					relayAddrInfo, err := peer.AddrInfoFromString(relayAddr)
					if err != nil {
						log.Printf("Failed to parse relay address %s: %v", relayAddr, err)
						continue
					}

					relayCtx, relayCancel := context.WithTimeout(stream.Context(), 20*time.Second)
					if err := s.node.Connect(relayCtx, *relayAddrInfo); err != nil {
						log.Printf("Relay connection via %s failed: %v", relayPeer, err)
						relayCancel()
					} else {
						log.Printf("Relay connection via %s SUCCESSFUL!", relayPeer)
						connectionSuccessful = true
						connectionMethod = fmt.Sprintf("relay via %s", relayPeer)
						relayCancel()
						break
					}
				}
			}

			// Final connection validation
			if !connectionSuccessful {
				errMsg := fmt.Sprintf("Failed to establish connection to session Admin %s after trying direct and relay methods. Both peers may be behind symmetric NATs.", adminInfo.ID)
				log.Println(errMsg)
				resp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: resp}}); errSend != nil {
					log.Printf("Error sending JoinSession error response (connection failed): %v", errSend)
				}
				continue
			}

			log.Printf("Connection ESTABLISHED to Admin %s using %s method. Connectivity: %s",
				adminInfo.ID, connectionMethod, s.node.Network().Connectedness(adminInfo.ID))
			log.Printf("Successfully connected to Admin %s for session %s. Proceeding to /auth/1 protocol.", adminInfo.ID, sessionIDToJoin)

			// --- Phase 2: Task 4 (Initiate /auth/1) ---
			authStreamCtx, authStreamCancel := context.WithTimeout(stream.Context(), 30*time.Second) // Timeout for auth exchange
			defer authStreamCancel()

			authStream, err := s.node.NewStream(authStreamCtx, adminInfo.ID, "/p2ptogether/auth/1")
			if err != nil {
				errMsg := fmt.Sprintf("Failed to open /auth/1 stream to Admin %s: %v", adminInfo.ID, err)
				log.Println(errMsg)
				resp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: resp}}); errSend != nil {
					log.Printf("Error sending JoinSession error response (auth stream open failed): %v", errSend)
				}
				continue
			}
			log.Printf("Opened /auth/1 stream to Admin %s", adminInfo.ID)

			// --- Send AuthRequest ---
			authReq := &p2ppb.AuthRequest{
				SessionId: sessionIDToJoin,
				Username:  joiningUsername,
			}
			authReqBytes, err := proto.Marshal(authReq)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to marshal AuthRequest: %v", err)
				log.Println(errMsg)
				resp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: resp}}); errSend != nil {
					log.Printf("Error sending JoinSession error response (marshal AuthRequest failed): %v", errSend)
				}
				authStream.Reset() // Or Close, Reset is more abrupt
				continue
			}

			_, err = authStream.Write(authReqBytes)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to write AuthRequest to Admin %s: %v", adminInfo.ID, err)
				log.Println(errMsg)
				resp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: resp}}); errSend != nil {
					log.Printf("Error sending JoinSession error response (auth write failed): %v", errSend)
				}
				authStream.Reset()
				continue
			}
			authStream.CloseWrite() // Signal Admin we're done writing
			log.Printf("Sent AuthRequest to Admin %s for session %s. Username: %s", adminInfo.ID, sessionIDToJoin, joiningUsername)

			// --- Read JoinSessionResponse from Admin ---
			authRespBuf := make([]byte, 4096) // Adjust buffer size if snapshot is larger
			authRespN, err := authStream.Read(authRespBuf)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to read JoinSessionResponse from Admin %s: %v", adminInfo.ID, err)
				log.Println(errMsg)
				// Send error to GUI
				guiResp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: guiResp}}); errSend != nil {
					log.Printf("Error sending JoinSession error response (auth read failed): %v", errSend)
				}
				authStream.Reset()
				continue
			}

			var adminJoinResp p2ppb.JoinSessionResponse // Variable to store Admin's response
			if err := proto.Unmarshal(authRespBuf[:authRespN], &adminJoinResp); err != nil {
				errMsg := fmt.Sprintf("Failed to unmarshal JoinSessionResponse from Admin %s: %v", adminInfo.ID, err)
				log.Println(errMsg)
				guiResp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(errMsg)}
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: guiResp}}); errSend != nil {
					log.Printf("Error sending JoinSession error response (auth unmarshal failed): %v", errSend)
				}
				authStream.Reset()
				continue
			}
			authStream.Close() // We've received the response.

			log.Printf("Received JoinSessionResponse from Admin %s. SessionID: %s, Error: %s", adminInfo.ID, adminJoinResp.GetSessionId(), adminJoinResp.GetErrorMessage())

			if adminJoinResp.GetErrorMessage() != "" {
				// Admin denied join or encountered an error, forward to GUI
				if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: &adminJoinResp}}); errSend != nil {
					log.Printf("Error sending Admin's error JoinSessionResponse to GUI: %v", errSend)
				}
				continue
			}

			// --- Process successful JoinSessionResponse from Admin ---
			s.processAdminJoinResponse(&adminJoinResp) // Call new helper function

			// Send the Admin's response (which includes the snapshot) to the local GUI.
			if errSend := stream.Send(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_JoinSessionResponse{JoinSessionResponse: &adminJoinResp}}); errSend != nil {
				log.Printf("Error sending final JoinSessionResponse to GUI: %v", errSend)
			}
			continue

		case *clientpb.ClientMsg_QueueCmd:
			if err := s.queueCtrl.Handle(stream.Context(), cmd.QueueCmd, senderID, s.roleManager, s.hub, s.node, s.node.CtrlTopic()); err != nil {
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

		case *clientpb.ClientMsg_ChatCmd:
			chatCmdProto := cmd.ChatCmd
			log.Printf("[gRPC Recv %s] Processing ChatCmd: Text='%.50s', HLC: %d", nodeIDStr, chatCmdProto.Text, chatCmdProto.HlcTs)

			// Create p2ppb.ChatMsg for broadcast
			chatMsgProto := &p2ppb.ChatMsg{
				MessageId: uuid.NewString(), // Generate a unique ID for the message
				Sender:    senderID.String(),
				Text:      chatCmdProto.Text,
				HlcTs:     chatCmdProto.HlcTs,
				// ReplyTo: chatCmdProto.ReplyTo, // If/when supporting replies
			}
			serverPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_ChatMsg{ChatMsg: chatMsgProto}}

			// Broadcast to local hub first for immediate echo
			s.hub.Broadcast(serverPayload)

			// Publish to chat topic
			_ = s.publishToChatTopic(stream.Context(), serverPayload) // Error already logged by helper

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
	ctrlTopic := s.node.CtrlTopic()
	if ctrlTopic == nil {
		return fmt.Errorf("control topic is nil, cannot publish")
	}
	marshalledMsg, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal ServerMsg for broadcast: %w", err)
	}
	if err := ctrlTopic.Publish(ctx, marshalledMsg); err != nil {
		return fmt.Errorf("failed to publish ServerMsg to ctrlTopic: %w", err)
	}
	return nil
}

// Helper to publish any ServerMsg (specifically ChatMsg) payload to the chat topic
func (s *server) publishToChatTopic(ctx context.Context, msg *clientpb.ServerMsg) error {
	// chatTopic := s.chatTopic // Use the server's view of the current topic
	chatTopic := s.node.ChatTopic() // More robust: get current topic from node
	if chatTopic == nil {
		return fmt.Errorf("chat topic is nil, cannot publish chat message")
	}
	topicPeers := chatTopic.ListPeers()
	log.Printf("[gossip ChatPublish] Attempting to publish to topic %s. Connected peers on this topic by PubSub: %d. Peers: %v", chatTopic.String(), len(topicPeers), topicPeers)

	marshalledMsg, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal ServerMsg (ChatMsg) for chat broadcast: %w", err)
	}
	if err := chatTopic.Publish(ctx, marshalledMsg); err != nil {
		return fmt.Errorf("failed to publish ServerMsg (ChatMsg) to chatTopic: %w", err)
	}
	return nil
}

// processAdminJoinResponse handles the successful JoinSessionResponse received from the Admin.
// This is called by the Joiner's Peer Service.
func (s *server) processAdminJoinResponse(adminResp *p2ppb.JoinSessionResponse) {
	log.Printf("Joiner: Processing successful JoinSessionResponse from Admin. SessionID: %s. Roles: %v. Snapshot size: %d bytes",
		adminResp.GetSessionId(), adminResp.GetAssignedRoles(), len(adminResp.GetSessionSnapshot()))

	// Store current session ID in the node
	s.node.SetCurrentSessionID(adminResp.GetSessionId())

	// Apply snapshot if present
	if len(adminResp.GetSessionSnapshot()) > 0 {
		var sessionStateData p2ppb.SessionStateData
		if err := proto.Unmarshal(adminResp.GetSessionSnapshot(), &sessionStateData); err != nil {
			log.Printf("Joiner: Failed to unmarshal SessionStateData from snapshot: %v. Proceeding without applying snapshot.", err)
		} else {
			log.Printf("Joiner: Successfully unmarshalled SessionStateData. Applying state...")
			if snapRoles := sessionStateData.GetAllRoleAssignments(); snapRoles != nil { // Use renamed field
				if err := s.roleManager.ApplyAllAssignmentsSnapshot(snapRoles); err != nil {
					log.Printf("Joiner: Error applying role assignments snapshot: %v", err)
				} else {
					log.Printf("Joiner: Applied role assignments snapshot. Local roles may have changed.")
				}
			}
			if snapQueue := sessionStateData.GetCurrentQueueState(); snapQueue != nil {
				s.queueCtrl.ApplyUpdate(snapQueue, s.hub, s.node, s.roleManager)
				log.Printf("Joiner: Applied queue state snapshot.")
			}
			// TODO: Apply playback state if present (sessionStateData.GetCurrentPlaybackState())
		}
	}

	// Subscribe to session-specific topics
	if err := s.node.JoinSessionTopics(adminResp.GetSessionId()); err != nil {
		log.Printf("Joiner: Failed to join session-specific pubsub topics for session %s: %v", adminResp.GetSessionId(), err)
	} else {
		// Update server's view of current topics for this joiner node
		// This is important if the server struct's s.ctrlTopic/s.chatTopic are used by handlers
		s.ctrlTopic = s.node.CtrlTopic()
		s.chatTopic = s.node.ChatTopic()
	}
}

// handleAuthStream is called by the Admin peer when a Joiner opens a /p2ptogether/auth/1 stream.
func (s *server) handleAuthStream(stream network.Stream) {
	remotePeerID := stream.Conn().RemotePeer()
	log.Printf("Admin: Received incoming /auth/1 stream from %s", remotePeerID)
	defer stream.Close() // Ensure stream is closed eventually

	// Read AuthRequest
	// Assuming AuthRequest is relatively small, read it all.
	// For larger messages, use a buffered reader or length-prefixing.
	buf := make([]byte, 1024) // Max AuthRequest size
	n, err := stream.Read(buf)
	if err != nil {
		log.Printf("Admin: Error reading AuthRequest from %s: %v", remotePeerID, err)
		// Cannot send a JoinSessionResponse here easily if read fails before unmarshalling.
		// The Joiner will experience a stream read error or timeout.
		return
	}

	var authReq p2ppb.AuthRequest
	if err := proto.Unmarshal(buf[:n], &authReq); err != nil {
		log.Printf("Admin: Error unmarshalling AuthRequest from %s: %v", remotePeerID, err)
		return
	}
	log.Printf("Admin: Received AuthRequest from %s for SessionID: %s, Username: %s", remotePeerID, authReq.GetSessionId(), authReq.GetUsername())

	// Validate SessionID
	session, exists := s.sessionManager.GetSession(authReq.GetSessionId())
	if !exists {
		log.Printf("Admin: Auth attempt from %s for non-existent session %s.", remotePeerID, authReq.GetSessionId())
		errResp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String("Session not found.")}
		errRespBytes, _ := proto.Marshal(errResp)
		_, _ = stream.Write(errRespBytes) // Best effort
		return
	}

	if session.AdminPeerID != s.node.ID() {
		log.Printf("Admin: Auth attempt for session %s, but this peer (%s) is not the admin (%s). Denying.", authReq.GetSessionId(), s.node.ID(), session.AdminPeerID)
		errResp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String("Authorization failed: not session admin.")}
		errRespBytes, _ := proto.Marshal(errResp)
		_, _ = stream.Write(errRespBytes) // Best effort
		return
	}

	// Add peer to session with default "Viewer" role
	_, _, err = s.sessionManager.AddPeerToSession(session.ID, remotePeerID, authReq.GetUsername(), []string{"Viewer"})
	if err != nil {
		log.Printf("Admin: Failed to add peer %s to session %s: %v", remotePeerID, session.ID, err)
		errResp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String(fmt.Sprintf("Failed to add peer to session: %v", err))}
		errRespBytes, _ := proto.Marshal(errResp)
		_, _ = stream.Write(errRespBytes) // Best effort
		return
	}

	// --- Populate SessionStateData for the snapshot ---
	allAssignments := s.roleManager.GetAllAssignments() // This returns []*p2ppb.PeerRoleAssignment
	allAssignmentsPb := &p2ppb.AllPeerRoleAssignments{
		Assignments: allAssignments, // Directly use the slice
		HlcTs:       common.GetCurrentHLC(),
	}

	queueItems := s.queueCtrl.Q().Items() // This returns []p2p.QueueItem (internal type)
	pbQueueItems := make([]*p2ppb.QueueItem, 0, len(queueItems))
	for _, qi := range queueItems {
		pbQueueItems = append(pbQueueItems, &p2ppb.QueueItem{
			FilePath: qi.FilePath,
			StoredBy: qi.StoredBy.String(),
			AddedBy:  qi.AddedBy.String(),
			FirstSeq: qi.FirstSeq,
			NumSegs:  qi.NumSegs,
			HlcTs:    qi.HlcTs,
		})
	}
	queueUpdatePb := &p2ppb.QueueUpdate{Items: pbQueueItems, HlcTs: common.GetCurrentHLC()}

	// TODO: Populate current_playback_state if/when GetLastKnownPlaybackState is robust
	// playbackStateInfo := s.queueCtrl.GetLastKnownPlaybackState()
	// var currentPlaybackCmd *p2ppb.SetPlaybackStateCmd
	// if playbackStateInfo.HlcTs > 0 { // Example condition: only send if state is somewhat initialized
	// 	currentPlaybackCmd = &p2ppb.SetPlaybackStateCmd{
	// 		TargetTimePos:    0, // This needs to be converted from PlaybackStateInfo
	// 		TargetIsPlaying:  playbackStateInfo.IsPlaying,
	// 		TargetSpeed:      1.0, // This needs to be converted
	// 		HlcTs:            playbackStateInfo.HlcTs,
	// 		StreamSequenceId: playbackStateInfo.StreamSequenceID,
	// 	}
	// }

	// Populate all_peer_identities for the snapshot
	allIdentities := make([]*p2ppb.LocalPeerIdentity, 0, len(session.Members))
	for pid, uinfo := range session.Members {
		allIdentities = append(allIdentities, &p2ppb.LocalPeerIdentity{
			PeerId:   pid.String(),
			Username: &uinfo.Username,
		})
	}

	sessionStateData := &p2ppb.SessionStateData{
		AllRoleAssignments: allAssignmentsPb,
		CurrentQueueState:  queueUpdatePb,
		// CurrentPlaybackState: currentPlaybackCmd, // Add when ready
	}
	sessionStateData.AllPeerIdentities = allIdentities
	snapshotBytes, errMarshal := proto.Marshal(sessionStateData)
	if errMarshal != nil {
		log.Printf("Admin: Failed to marshal SessionStateData for peer %s: %v", remotePeerID, errMarshal)
		// Send error or simple success without snapshot
		errResp := &p2ppb.JoinSessionResponse{ErrorMessage: proto.String("Internal server error creating session snapshot.")}
		errRespBytes, _ := proto.Marshal(errResp)
		_, _ = stream.Write(errRespBytes)
		return
	}

	successResp := &p2ppb.JoinSessionResponse{
		SessionId:       session.ID,
		AssignedRoles:   []string{"Viewer"},
		SessionSnapshot: snapshotBytes,
		SessionName:     proto.String(session.Name),
	}
	successRespBytes, _ := proto.Marshal(successResp)
	if _, err := stream.Write(successRespBytes); err != nil {
		log.Printf("Admin: Error writing success JoinSessionResponse to %s: %v", remotePeerID, err)
	}
	log.Printf("Admin: Peer %s successfully authenticated and added to session %s as Viewer. Snapshot sent (%d bytes).", remotePeerID, session.ID, len(snapshotBytes))

	// Broadcast PeerRoleAssignment and LocalPeerIdentity for the new joiner
	newMemberAssignment := &p2ppb.PeerRoleAssignment{
		PeerId:            remotePeerID.String(),
		AssignedRoleNames: []string{"Viewer"}, // Roles assigned during AddPeerToSession
		HlcTs:             common.GetCurrentHLC(),
	}
	newMemberIdentity := &p2ppb.LocalPeerIdentity{
		PeerId:   remotePeerID.String(),
		Username: &authReq.Username, // Username from their AuthRequest
	}

	serverMsgIdentityPayload := &clientpb.ServerMsg{
		Payload: &clientpb.ServerMsg_LocalPeerIdentity{LocalPeerIdentity: newMemberIdentity},
	}
	serverMsgPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PeerRoleAssignment{PeerRoleAssignment: newMemberAssignment}}

	// Broadcast to Admin's own GUI
	s.hub.Broadcast(serverMsgPayload)
	s.hub.Broadcast(serverMsgIdentityPayload)

	// Broadcast to other peers in the session via GossipSub control topic
	// TODO: Ensure s.ctrlTopic is correctly initialized and session-specific if needed
	ctrlTopic := s.node.CtrlTopic() // Get current session's control topic
	if ctrlTopic != nil {
		if err := s.publishToControlTopic(context.Background(), serverMsgPayload); err != nil { // Using Background context for now
			log.Printf("Admin: Error publishing PeerRoleAssignment for %s to control topic: %v", remotePeerID, err)
		} else {
			log.Printf("Admin: Published PeerRoleAssignment for new joiner %s to control topic.", remotePeerID)
		}
		if err := s.publishToControlTopic(context.Background(), serverMsgIdentityPayload); err != nil {
			log.Printf("Admin: Error publishing LocalPeerIdentity for %s to control topic: %v", remotePeerID, err)
		} else {
			log.Printf("Admin: Published LocalPeerIdentity for new joiner %s to control topic.", remotePeerID)
		}
	} else {
		log.Printf("Admin: Control topic is nil, cannot publish updates for %s.", remotePeerID)
	}
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
// Returns host, dht, and *drouting.RoutingDiscovery
func buildHost(ctx context.Context) (host.Host, *dht.IpfsDHT, *drouting.RoutingDiscovery) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic",
			"/ip6/::/tcp/0",
			"/ip6/::/udp/0/quic",
		),
		libp2p.EnableNATService(),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		// libp2p.ForceReachabilityPrivate(),
		libp2p.DefaultTransports,
	)
	if err != nil {
		log.Fatalf("libp2p host init failed: %v", err)
	}

	// kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	// kademliaDHT, err := dht.New(ctx, h)
	if err != nil {
		log.Fatalf("Kademlia DHT init failed: %v", err)
	}

	// Connect to an initial set of bootstrap peers.
	// DefaultBootstrapPeers are public P2P nodes that help new peers join the network.
	log.Println("Connecting to bootstrap peers...")
	var wg sync.WaitGroup
	successfulConnections := 0
	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerInfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			log.Printf("Error parsing bootstrap peer address %s: %v", peerAddr, err)
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			// Use a timeout for each connection attempt to avoid indefinite blocking.
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := h.Connect(connectCtx, pi); err != nil {
				log.Printf("Warning: Failed to connect to bootstrap peer %s: %v", pi.ID, err)
			} else {
				log.Printf("Successfully connected to bootstrap peer: %s", pi.ID)
				successfulConnections++
			}
		}(*peerInfo)
	}
	wg.Wait()
	log.Printf("Finished attempting to connect to bootstrap peers. Success: %d/%d", successfulConnections, len(dht.DefaultBootstrapPeers))

	// Bootstrap the DHT. This will populate the DHT routing table with peers.
	log.Println("Bootstrapping DHT...")
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		log.Fatalf("DHT bootstrap failed: %v", err)
	}

	// Give DHT some time to populate routing table
	log.Println("Waiting for DHT to populate routing table...")
	time.Sleep(2 * time.Second)
	routingTableSize := kademliaDHT.RoutingTable().Size()
	log.Printf("DHT bootstrap completed. Routing table size: %d", routingTableSize)

	if routingTableSize == 0 {
		log.Printf("Warning: DHT routing table is empty after bootstrap. This may affect discovery.")
	}

	log.Printf("Libp2p Host created with ID: %s", h.ID().String())
	for _, addr := range h.Addrs() {
		log.Printf("Listening on address: %s/p2p/%s", addr, h.ID().String())
	}

	// return h, kademliaDHT
	routingDiscovery := drouting.NewRoutingDiscovery(kademliaDHT)

	return h, kademliaDHT, routingDiscovery
}

// mDNS is still useful for local LAN discovery or complementing DHT for WAN.
func startMdnsDiscovery(h host.Host, dht *dht.IpfsDHT) {
	mdnsSvc := mdns.NewMdnsService(h, "p2ptogether-mdns", &mdnsNotifee{h: h, dht: dht})
	if err := mdnsSvc.Start(); err != nil {
		log.Printf("mDNS start failed: %v (continuing without mDNS)", err)
	}
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
		case *clientpb.ServerMsg_PlaylistReset:
			handleGossipPlaylistReset(payload.PlaylistReset, msg.ReceivedFrom, srv)
		case *clientpb.ServerMsg_PlaybackStateCmd:
			handleGossipPlaybackStateCmd(payload.PlaybackStateCmd, msg.ReceivedFrom, srv)
		case *clientpb.ServerMsg_LocalPeerIdentity:
			handleGossipLocalPeerIdentity(payload.LocalPeerIdentity, msg.ReceivedFrom, srv)
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

		currentChatTopicSub := sub // The subscription object itself
		if currentChatTopicSub != nil && srv.node != nil && srv.node.PubSubInstance() != nil {
			log.Printf("[gossip ChatRecv] Node %s processing message on topic %s. Known peers by PubSub for this topic: %v", myID, currentChatTopicSub.Topic(), srv.node.PubSubInstance().ListPeers(currentChatTopicSub.Topic()))
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

// handleGossipPlaylistReset resets the local RingBuffer and HLS state in response to a remote command.
func handleGossipPlaylistReset(cmd *p2ppb.PlaylistReset, from peer.ID, srv *server) {
	log.Printf("[gossip Ctrl] handleGossipPlaylistReset: received from %s (Seq: %d, HLC: %d)", from, cmd.Sequence, cmd.HlcTs)
	// Reset the local RingBuffer to the new base sequence. This purges old segments.
	if rb := srv.node.RingBuffer(); rb != nil {
		rb.Reset(cmd.Sequence)
	}
	// Trigger a discontinuity in the local HLS server.
	srv.triggerHLSDircontinuity()
	// Forward the message to the local GUI client.
	srv.hub.Broadcast(&clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaylistReset{PlaylistReset: cmd}})
}

// handleGossipPlaybackStateCmd forwards a SetPlaybackStateCmd received via GossipSub to the local GUI via the Hub.
func handleGossipPlaybackStateCmd(cmd *p2ppb.SetPlaybackStateCmd, from peer.ID, srv *server) {
	log.Printf("[gossip PlaybackRecv] Received SetPlaybackStateCmd (Play:%v Pos:%.2f Speed:%.2f HLC:%d StreamSeq: %d) from %s.",
		cmd.TargetIsPlaying, cmd.TargetTimePos, cmd.TargetSpeed, cmd.HlcTs, cmd.StreamSequenceId, from)

	// Update QueueController's state *before* broadcasting to local GUI.
	srv.queueCtrl.UpdateLastKnownPlaybackState(cmd)

	serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaybackStateCmd{PlaybackStateCmd: cmd}} // Updated ServerMsg field

	srv.hub.Broadcast(serverMsg) // Send to the connected GUI client(s)
}

// handleGossipLocalPeerIdentity processes a LocalPeerIdentity message received via GossipSub.
// This is typically for learning about other peers' usernames.
func handleGossipLocalPeerIdentity(identity *p2ppb.LocalPeerIdentity, from peer.ID, srv *server) {
	log.Printf("[gossip Ctrl] handleGossipLocalPeerIdentity: for peer %s (user: %s) from %s", identity.PeerId, identity.GetUsername(), from)

	// We just need to forward this to our local GUI client.
	// The GUI client's RoleStore will handle storing the username.
	// The client's App will update its own m_localUsername if this identity.PeerId matches its own.
	serverMsg := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_LocalPeerIdentity{LocalPeerIdentity: identity}}
	srv.hub.Broadcast(serverMsg)
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

	// Make ffmpeg discoverable when bundled with the application.
	// Prepend the executable's directory to the PATH environment variable.
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		currentPath := os.Getenv("PATH")
		newPath := fmt.Sprintf("%s%c%s", exeDir, os.PathListSeparator, currentPath)
		if err := os.Setenv("PATH", newPath); err != nil {
			log.Printf("Warning: Failed to set new PATH: %v", err)
		}
	}

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
	lhost, kadDHT, routingDiscovery := buildHost(ctx)
	ps, err := pubsub.NewGossipSub(ctx, lhost)
	// startMdnsDiscovery(lhost, kadDHT)
	if err != nil {
		log.Fatalf("GossipSub init failed: %v", err)
	}
	node := p2p.NewNode(lhost, ps, hlsPort, rb)
	hub := p2p.NewHub()
	queueCtrl := p2p.NewQueueController(triggerDiscontinuity)
	roleManager := roles.NewRoleManager()
	sessionMgr := session.NewSessionManager(roleManager)

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

	// clientpb.RegisterP2PTClientServer(grpcServer, &server{
	// Define the server instance that holds all dependencies
	srvInstance := &server{
		hlsPort:                 hlsPort,
		hub:                     hub,
		queueCtrl:               queueCtrl,
		node:                    node,
		dht:                     kadDHT,
		sessionManager:          sessionMgr,
		roleManager:             roleManager,
		routingDiscovery:        routingDiscovery,
		triggerHLSDircontinuity: triggerDiscontinuity,
	}
	clientpb.RegisterP2PTClientServer(grpcServer, srvInstance)

	// Initialize node's default topics (it will manage them)
	// These are "default" topics before any session is joined/created.
	// Node.JoinSessionTopics will handle leaving these and joining specific ones.
	if err := node.JoinSessionTopics("default"); err != nil {
		log.Printf("Warning: Failed to join initial 'default' topics: %v", err)
	}
	// Set server's initial topics from the node's current topics (which are "default")
	srvInstance.ctrlTopic = node.CtrlTopic()
	srvInstance.chatTopic = node.ChatTopic()

	// Pass the node's getter for ctrlTopic, so QueueController always uses the current one
	queueCtrl.SetDependencies(node, hub, roleManager, node.CtrlTopic())

	// Register IngestHandler with the fully initialized node (which can provide durations)
	httpMux.HandleFunc("/ingest/", media.IngestHandler(rb, srvInstance.node))

	// Register the /auth/1 stream handler
	lhost.SetStreamHandler("/p2ptogether/auth/1", srvInstance.handleAuthStream)

	go func() { // Goroutine for control messages
		var sub *pubsub.Subscription
		var currentTopicName string
		for {
			select {
			case <-ctx.Done():
				if sub != nil {
					sub.Cancel()
				}
				return
			default:
				topic := srvInstance.node.CtrlTopic()
				if topic != nil && (sub == nil || topic.String() != currentTopicName) {
					if sub != nil {
						sub.Cancel()
					}
					var errSub error
					sub, errSub = topic.Subscribe()
					if errSub != nil {
						log.Printf("Error subscribing to control topic %s: %v. Retrying...", topic.String(), errSub)
						time.Sleep(5 * time.Second)
						continue
					}
					currentTopicName = topic.String()
					log.Printf("Subscribed to control topic: %s", currentTopicName)
					go processControlTopicMessages(ctx, sub, srvInstance) // process in its own goroutine to not block re-subscription
				}
				time.Sleep(1 * time.Second) // Check for topic changes periodically
			}
		}
	}()

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

	// Goroutine for chat messages - more robust dynamic subscription
	go func() {
		var sub *pubsub.Subscription
		var currentTopicName string
		defer func() {
			if sub != nil {
				sub.Cancel()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				topic := srvInstance.node.ChatTopic() // Get current chat topic from node
				if topic != nil && (sub == nil || topic.String() != currentTopicName) {
					if sub != nil {
						sub.Cancel() // Cancel previous subscription
					}
					var errSub error
					sub, errSub = topic.Subscribe()
					if errSub != nil {
						log.Printf("Error subscribing to chat topic %s: %v. Retrying...", topic.String(), errSub)
						time.Sleep(5 * time.Second)
						continue // Retry subscription
					}
					currentTopicName = topic.String()
					log.Printf("Subscribed to chat topic: %s", currentTopicName)
					// Dedicate a goroutine to pump messages from this specific subscription
					// to avoid blocking the re-subscription loop.
					go processChatTopicMessages(ctx, sub, srvInstance)
				} else if topic == nil && sub != nil { // Current session ended, no topic
					sub.Cancel()
					sub = nil
					currentTopicName = ""
				}
				time.Sleep(1 * time.Second) // Poll for topic changes
			}
		}
	}()

	// Goroutine for video segment messages
	go func() {
		var sub *pubsub.Subscription
		var currentTopicName string
		defer func() {
			if sub != nil {
				sub.Cancel()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				topic := srvInstance.node.VideoTopic() // Get current video topic from node
				if topic != nil && (sub == nil || topic.String() != currentTopicName) {
					if sub != nil {
						sub.Cancel() // Cancel previous subscription
					}
					var errSub error
					sub, errSub = topic.Subscribe()
					if errSub != nil {
						log.Printf("Error subscribing to video topic %s: %v. Retrying...", topic.String(), errSub)
						time.Sleep(5 * time.Second)
						continue // Retry subscription
					}
					currentTopicName = topic.String()
					log.Printf("Subscribed to video topic: %s", currentTopicName)
					go srvInstance.processVideoSegmentMessages(ctx, sub, rb) // Process messages in a new goroutine
				} else if topic == nil && sub != nil { // Current session ended
					sub.Cancel()
					sub = nil
					currentTopicName = ""
				}
				time.Sleep(1 * time.Second) // Poll for topic changes
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
