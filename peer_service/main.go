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

	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"peer_service/internal/media"
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
	log.Printf("Client %s connected.", pid.String()) // Log connection
	defer func() {
		s.hub.Remove(pid.String())
		log.Printf("Client %s removed from hub.", pid.String()) // Log removal
	}()

	for {
		in, err := stream.Recv()
		if err != nil {
			// Log client disconnection reason
			log.Printf("Client %s disconnected: %v", pid.String(), err)
			return err // Return error to signal stream closure
		}

		switch x := in.Payload.(type) {
		case *pb.ClientMsg_QueueCmd:
			if err := s.ctrl.Handle(x.QueueCmd, pid, roles.All, s.hub); err != nil {
				log.Printf("Error handling QueueCmd from %s: %v", pid.String(), err)
				// Decide if the error is fatal for this stream
				// return err // Example: return error to close stream on failure
			}
		default:
			log.Printf("Received unhandled message type from %s", pid.String())
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
	log.Printf("HLS endpoint listening on %s\n", httpLn.Addr().String())

	// --- mini‑HLS in‑RAM buffer ---
	rb := media.NewRingBuffer(120) // 120 s window
	plH, segH := media.Handler(rb)

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
	ctrl := p2p.NewQueueController()

	pb.RegisterP2PTClientServer(grpcServer, &server{
		hlsPort: hlsPort,
		hub:     hub,
		ctrl:    ctrl,
	})

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

	// 3) Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	sig := <-stopChan                                                // Block until a signal is received
	log.Printf("Received signal: %v. Shutting down servers...", sig) // Log received signal

	// Shutdown gRPC server - Use Stop() instead of GracefulStop()
	log.Println("Attempting gRPC server Stop()...")
	grpcServer.Stop()                            // Force immediate stop
	log.Println("gRPC server Stop() completed.") // Log completion

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
	time.Sleep(1 * time.Second)

	log.Println("Peer service exited gracefully.")
}
