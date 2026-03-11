package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"google.golang.org/protobuf/proto"

	"peer_service/internal/common"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"
)

// ScenarioPeer represents the state of our test peer
type ScenarioPeer struct {
	Host        host.Host
	PubSub      *pubsub.PubSub
	ChatTopic   *pubsub.Topic
	CtrlTopic   *pubsub.Topic
	VideoTopic  *pubsub.Topic
	ChatSub     *pubsub.Subscription
	CtrlSub     *pubsub.Subscription
	Ctx         context.Context
	Cancel      context.CancelFunc
	SessionID   string
	MyUsername  string
	DesiredRole string // Role to behave as (Admin, Moderator, Streamer, Viewer)
}

func main() {
	// Flags
	listenF := flag.String("listen", "/ip4/0.0.0.0/tcp/0", "libp2p listen address")
	connectF := flag.String("connect", "", "Comma-separated multiaddrs to connect to")
	sessionF := flag.String("session", "default-session", "Session ID to join or create")
	nickF := flag.String("nickname", "dummypeer", "Nickname for this peer")
	roleF := flag.String("role", "Viewer", "Role behavior to adopt (Admin, Moderator, Streamer, Viewer)")
	createF := flag.Bool("create", false, "Create the session instead of joining")
	flag.Parse()

	// Validate Role
	validRole := false
	rm := roles.NewRoleManager() // Use a temporary manager just to get default definitions
	for _, r := range rm.GetDefinitions() {
		if strings.EqualFold(r.Name, *roleF) {
			*roleF = r.Name // Normalize casing
			validRole = true
			break
		}
	}
	if !validRole {
		log.Fatalf("Invalid role: %s. Must be one of: Admin, Moderator, Streamer, Viewer", *roleF)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Setup Host
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, rand.Reader)
	if err != nil {
		log.Fatalf("Failed to gen key: %v", err)
	}

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(*listenF),
		libp2p.Identity(priv),
	)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	log.Printf("I am %s aka '%s' acting as '%s'", h.ID(), *nickF, *roleF)
	log.Printf("Listening on: %v", h.Addrs())

	sp := &ScenarioPeer{
		Host:        h,
		Ctx:         ctx,
		Cancel:      cancel,
		SessionID:   *sessionF,
		MyUsername:  *nickF,
		DesiredRole: *roleF,
	}

	// 2. Connect to peers
	if *connectF != "" {
		addrs := strings.Split(*connectF, ",")
		var wg sync.WaitGroup
		for _, s := range addrs {
			ma, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				log.Printf("Invalid multiaddr %s: %v", s, err)
				continue
			}
			peerInfo, err := peer.AddrInfoFromP2pAddr(ma)
			if err != nil {
				log.Printf("Failed to get addr info from %s: %v", s, err)
				continue
			}

			wg.Add(1)
			go func(pi peer.AddrInfo) {
				defer wg.Done()
				if err := h.Connect(ctx, pi); err != nil {
					log.Printf("Failed to connect to %s: %v", pi.ID, err)
				} else {
					log.Printf("Connected to %s", pi.ID)
				}
			}(*peerInfo)
		}
		wg.Wait()
	}

	// 3. Init PubSub
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		log.Fatalf("Failed to create gossipsub: %v", err)
	}
	sp.PubSub = ps

	// 4. Session Logic (Create or Join)
	// Register Auth Handler (Server-side) in case we are the session host
	h.SetStreamHandler("/p2ptogether/auth/1", func(s network.Stream) {
		log.Printf("[AuthHandler] Incoming auth stream from %s", s.Conn().RemotePeer())
		defer s.Close()

		// Read AuthRequest
		buf := make([]byte, 1024)
		n, err := s.Read(buf)
		if err != nil {
			log.Printf("[AuthHandler] Read failed: %v", err)
			return
		}
		var req p2ppb.AuthRequest
		if err := proto.Unmarshal(buf[:n], &req); err != nil {
			log.Printf("[AuthHandler] Unmarshal failed: %v", err)
			return
		}
		log.Printf("[AuthHandler] Received AuthRequest from %s for session %s (User: %s)", s.Conn().RemotePeer(), req.SessionId, req.Username)

		// Verification Logic (Simple: Check session ID)
		// If we created the session or are in it, we might approve.
		// For this tool, we approve if req.SessionId matches ours (or we assume "verifysess" is valid)

		var errMsg *string
		if req.SessionId != *sessionF {
			msg := fmt.Sprintf("Session mismatch. I am in %s, you requested %s", *sessionF, req.SessionId)
			errMsg = &msg
		}

		resp := &p2ppb.JoinSessionResponse{
			SessionId: req.SessionId, // Confirm the ID
			// InviteCode: ...,
			ErrorMessage: errMsg,
		}

		data, _ := proto.Marshal(resp)
		s.Write(data)
		log.Printf("[AuthHandler] Sent response to %s", s.Conn().RemotePeer())
	})

	if *createF {
		log.Printf("Creating session '%s'...", *sessionF)
		// Creation logic is implicit for test tool: just join topics
		// In real app, Admin creates it.
		// Todo: Start advertising if we want others to find us via DHT
	} else {
		log.Printf("Joining session '%s'...", *sessionF)
		// Perform Handshake with a connected peer who knows the session
		performAuthHandshake(sp)
	}

	// 5. Join Topics
	joinTopics(sp)

	// 6. Interactive Loop / Script
	startInteractiveLoop(sp)
}

func performAuthHandshake(sp *ScenarioPeer) {
	// Simple strategy: try to auth with the first connected peer
	peers := sp.Host.Network().Peers()
	if len(peers) == 0 {
		log.Println("No peers connected. Skipping auth handshake (assuming local/solo test).")
		return
	}

	target := peers[0]
	log.Printf("Attempting Auth handshake with %s...", target)

	s, err := sp.Host.NewStream(sp.Ctx, target, "/p2ptogether/auth/1")
	if err != nil {
		log.Printf("Failed to open /auth/1 stream to %s: %v. (They might not support it)", target, err)
		return
	}
	defer s.Close()

	// Send AuthRequest
	req := &p2ppb.AuthRequest{
		SessionId: sp.SessionID,
		Username:  sp.MyUsername,
	}
	data, _ := proto.Marshal(req)
	_, err = s.Write(data)
	if err != nil {
		log.Printf("Failed to write AuthRequest: %v", err)
		return
	}
	// Signal done writing
	s.CloseWrite()

	// Read response
	buf, err := io.ReadAll(s)
	if err != nil {
		log.Printf("Failed to read AuthResponse: %v", err)
		return
	}

	var resp p2ppb.JoinSessionResponse
	if err := proto.Unmarshal(buf, &resp); err != nil {
		log.Printf("Failed to unmarshal JoinSessionResponse: %v", err)
		return
	}

	if resp.ErrorMessage != nil {
		log.Printf("Auth Handshake REFUSED: %s", *resp.ErrorMessage)
		os.Exit(1)
	}

	log.Printf("Auth Handshake SUCCESS! SessionID: %s", resp.GetSessionId())
}

func joinTopics(sp *ScenarioPeer) {
	var err error
	sp.ChatTopic, err = sp.PubSub.Join(fmt.Sprintf("/p2ptogether/chat/%s", sp.SessionID))
	if err != nil {
		log.Fatalf("Join chat failed: %v", err)
	}
	sp.ChatSub, err = sp.ChatTopic.Subscribe()
	if err != nil {
		log.Fatalf("Sub chat failed: %v", err)
	}

	sp.CtrlTopic, err = sp.PubSub.Join(fmt.Sprintf("/p2ptogether/control/%s", sp.SessionID))
	if err != nil {
		log.Fatalf("Join ctrl failed: %v", err)
	}
	sp.CtrlSub, err = sp.CtrlTopic.Subscribe()
	if err != nil {
		log.Fatalf("Sub ctrl failed: %v", err)
	}

	sp.VideoTopic, err = sp.PubSub.Join(fmt.Sprintf("/p2ptogether/video/%s", sp.SessionID))
	if err != nil {
		log.Fatalf("Join video failed: %v", err)
	}
	videoSub, err := sp.VideoTopic.Subscribe()
	if err != nil {
		log.Fatalf("Sub video failed: %v", err)
	}

	// Handler goroutines
	go func() {
		for {
			msg, err := sp.ChatSub.Next(sp.Ctx)
			if err != nil {
				return
			}
			var sm clientpb.ServerMsg
			if err := proto.Unmarshal(msg.Data, &sm); err == nil {
				if chatMsg, ok := sm.Payload.(*clientpb.ServerMsg_ChatMsg); ok {
					log.Printf("[CHAT] %s: %s", chatMsg.ChatMsg.Sender, chatMsg.ChatMsg.Text)
				} else {
					log.Printf("[PUB] Received other msg on ChatTopic")
				}
			} else {
				log.Printf("[PUB] Received raw/unknown on ChatTopic: %s", string(msg.Data))
			}
		}
	}()

	go func() {
		for {
			msg, err := sp.CtrlSub.Next(sp.Ctx)
			if err != nil {
				return
			}
			var sm clientpb.ServerMsg
			if err := proto.Unmarshal(msg.Data, &sm); err == nil {
				switch pl := sm.Payload.(type) {
				case *clientpb.ServerMsg_PeerRoleAssignment:
					log.Printf("[CTRL] Role Assignment: Peer %s -> Roles %v", pl.PeerRoleAssignment.PeerId, pl.PeerRoleAssignment.AssignedRoleNames)
				default:
					log.Printf("[CTRL] Received update: %T", sm.Payload)
				}
			}
		}
	}()

	go func() {
		for {
			msg, err := videoSub.Next(sp.Ctx)
			if err != nil {
				return
			}
			// Video messages are VideoSegmentGossip (raw on pubsub presumably? or wrapped?)
			// Protocol design check: Streaming usually sends p2ppb.VideoSegmentGossip directly or wrapped?
			// Checking streaming.proto again or just dumping type.
			// Let's assume it might be wrapped in ServerMsg OR just the struct.
			// But wait, the standard usually wraps if multiple types go to same topic, but VideoTopic is likely just segments.

			var seg p2ppb.VideoSegmentGossip
			if err := proto.Unmarshal(msg.Data, &seg); err == nil {
				log.Printf("[VIDEO] Received segment seq=%d len=%d duration=%f", seg.SequenceNumber, len(seg.Data), seg.ActualDurationSeconds)
			} else {
				log.Printf("[VIDEO] Received unknown data len=%d", len(msg.Data))
			}
		}
	}()

}

func startInteractiveLoop(sp *ScenarioPeer) {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Enter commands (chat <msg>, disconnect, q):")
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		cmd := parts[0]

		switch cmd {
		case "chat":
			if len(parts) < 2 {
				fmt.Println("Usage: chat <message>")
				continue
			}
			text := strings.Join(parts[1:], " ")
			// Construct ClientMsg -> But we are simulating a Peer Node, so we publish directly to PubSub as if we were the server?
			// Or do we act as a Server forwarding a Client's message?
			// In the architecture, the Server receives ClientMsg and publishes ServerMsg.
			// So `net_scenario` acting as a Peer Node should publish ServerMsg.

			msg := &clientpb.ServerMsg{
				Payload: &clientpb.ServerMsg_ChatMsg{
					ChatMsg: &p2ppb.ChatMsg{
						MessageId: fmt.Sprintf("%d", time.Now().UnixNano()),
						Sender:    sp.Host.ID().String(),
						Text:      text,
						HlcTs:     common.GetCurrentHLC(),
					},
				},
			}
			data, _ := proto.Marshal(msg)
			if err := sp.ChatTopic.Publish(sp.Ctx, data); err != nil {
				log.Printf("Failed to publish chat: %v", err)
			} else {
				log.Println("Chat published.")
			}

		case "disconnect":
			sp.Cancel()
			return
		case "q", "quit":
			sp.Cancel()
			return
		default:
			fmt.Println("Unknown command")
		}
	}
}
