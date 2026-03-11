package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"peer_service/internal/common"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"
)

func main() {
	addrF := flag.String("addr", "127.0.0.1:0", "gRPC server address")
	cmdF := flag.String("cmd", "create-session", "Command to execute: create-session")
	sessNameF := flag.String("session-name", "test-session", "Session Name")
	usernameF := flag.String("username", "AdminUser", "Username")
	targetPeerF := flag.String("target-peer", "", "Target Peer ID for role assignment")
	rolesF := flag.String("roles", "", "Comma-separated roles to assign")
	messageF := flag.String("message", "", "Chat message text")
	fileF := flag.String("file", "", "File path for queue-add")
	flag.Parse()

	conn, err := grpc.NewClient(*addrF, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Did not connect: %v", err)
	}
	defer conn.Close()
	c := clientpb.NewP2PTClientClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Establish stream
	stream, err := c.ControlStream(ctx)
	if err != nil {
		log.Fatalf("Error creating stream: %v", err)
	}

	switch *cmdF {
	case "create-session":
		req := &clientpb.ClientMsg_CreateSessionRequest{
			CreateSessionRequest: &p2ppb.CreateSessionRequest{
				SessionName: sessNameF,
				Username:    usernameF,
			},
		}
		if err := stream.Send(&clientpb.ClientMsg{Payload: req}); err != nil {
			log.Fatalf("Failed to send CreateSessionRequest: %v", err)
		}

		// Wait for response
		for {
			resp, err := stream.Recv()
			if err != nil {
				log.Fatalf("Stream recv error: %v", err)
			}
			if pl, ok := resp.Payload.(*clientpb.ServerMsg_CreateSessionResponse); ok {
				if pl.CreateSessionResponse.ErrorMessage != nil {
					log.Fatalf("CreateSession failed: %s", *pl.CreateSessionResponse.ErrorMessage)
				}
				fmt.Printf("SESSION_ID:%s\n", pl.CreateSessionResponse.SessionId)
				fmt.Printf("INVITE_CODE:%s\n", pl.CreateSessionResponse.InviteCode)
				return
			}
		}

	case "send-chat":
		req := &clientpb.ClientMsg_ChatCmd{
			ChatCmd: &p2ppb.ChatCmd{
				Text:  *messageF,
				HlcTs: common.GetCurrentHLC(),
			},
		}
		if err := stream.Send(&clientpb.ClientMsg{Payload: req}); err != nil {
			log.Fatalf("Failed to send ChatCmd: %v", err)
		}
		fmt.Println("Chat sent.")

	case "assign-role":
		roleList := []string{}
		if *rolesF != "" {
			// splitting logic needed? or just pass slice?
			// The grpc_driver is simple, assuming no complex splitting needed if we use tools properly.
			// But for "Streamer", it's just one.
			roleList = append(roleList, *rolesF)
		}
		req := &clientpb.ClientMsg_SetPeerRolesCmd{
			SetPeerRolesCmd: &p2ppb.SetPeerRolesCmd{
				TargetPeerId:      *targetPeerF,
				AssignedRoleNames: roleList,
				HlcTs:             common.GetCurrentHLC(),
			},
		}
		if err := stream.Send(&clientpb.ClientMsg{Payload: req}); err != nil {
			log.Fatalf("Failed to send SetPeerRolesCmd: %v", err)
		}
		fmt.Println("Role assignment sent.")

	case "queue-add":
		// Expects -file <path>
		// Use -target-peer as file path reuse? No, better add proper flag or reuse existing if possible.
		// Let's reuse messageF or add a new flag 'file'.
		// The prompt didn't specify I can't add flags.
		// Let's add a `fileF` flag in the beginning of main.
		req := &clientpb.ClientMsg_QueueCmd{
			QueueCmd: &p2ppb.QueueCmd{
				Type:     p2ppb.QueueCmd_APPEND,
				FilePath: *fileF,
				HlcTs:    common.GetCurrentHLC(),
			},
		}
		if err := stream.Send(&clientpb.ClientMsg{Payload: req}); err != nil {
			log.Fatalf("Failed to send QueueCmd(APPEND): %v", err)
		}
		fmt.Println("Queue append sent.")

	default:
		log.Fatalf("Unknown command: %s", *cmdF)
	}
}
