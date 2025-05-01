// peer_service/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"peer_service/internal/p2p"
	"peer_service/internal/roles"
	pb "peer_service/proto/client" // local import for client proto
)

// server implements the gRPC service defined in proto.
type server struct {
	pb.UnimplementedP2PTClientServer
	hlsPort uint32

	// fan-out for ServerMsg broadcasts
	hub *p2p.Hub

	// owns the in-memory Queue and applies QueueCmds
	ctrl *p2p.QueueController
}

func (s *server) GetServiceInfo(ctx context.Context, _ *emptypb.Empty) (*pb.ServiceInfo, error) {
	return &pb.ServiceInfo{HlsPort: s.hlsPort}, nil
}

func (s *server) ControlStream(stream pb.P2PTClient_ControlStreamServer) error {
	pid := peer.ID("stub") // later: real peer ID from auth layer
	s.hub.Add(pid.String(), stream)
	defer s.hub.Remove(pid.String())

	for {
		in, err := stream.Recv()
		if err != nil {
			return err
		}

		switch x := in.Payload.(type) {
		case *pb.ClientMsg_QueueCmd:
			if err := s.ctrl.Handle(x.QueueCmd, pid, roles.All, s.hub); err != nil {
				return err
			}
		}
	}
}

func main() {
	// flags
	var grpcPort int
	flag.IntVar(&grpcPort, "grpc-port", 8268, "gRPC listen port")
	flag.Parse()

	// 1) Start HTTP mini-HLS on a random port
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("HTTP listen failed: %v", err)
	}
	hlsPort := uint32(httpLn.Addr().(*net.TCPAddr).Port)
	log.Printf("HLS endpoint listening on 127.0.0.1:%d\n", hlsPort)

	// Serve placeholder playlist/segments
	http.HandleFunc("/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintln(w, "#EXTM3U\n# TODO serve real .m3u8")
	})
	go func() {
		if err := http.Serve(httpLn, nil); err != nil {
			log.Fatalf("HLS server error: %v", err)
		}
	}()

	// 2) Start gRPC server
	lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", grpcPort))
	if err != nil {
		log.Fatalf("gRPC listen failed: %v", err)
	}
	s := grpc.NewServer()
	// one Hub + one QueueController per peer-service instance
	hub := p2p.NewHub()
	ctrl := p2p.NewQueueController()

	pb.RegisterP2PTClientServer(s, &server{
		hlsPort: hlsPort,
		hub:     hub,
		ctrl:    ctrl,
	})
	log.Printf("gRPC server listening on 127.0.0.1:%d\n", grpcPort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("gRPC serve failed: %v", err)
	}
}
