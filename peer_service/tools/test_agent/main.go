package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

const (
	AgentProtocolID = "/p2ptogether-test/agent/1.0.0"
	MdnsServiceTag  = "_p2ptogether-test._tcp"
	GuiTestPort     = 19999
)

type Agent struct {
	Host           host.Host
	Ctx            context.Context
	Cancel         context.CancelFunc
	GUIProcess     *exec.Cmd
	GUIPort        int
	DefaultBinPath string
	mu             sync.Mutex
}

func main() {
	port := flag.Int("port", 0, "libp2p listen port (0 for random)")
	guiBin := flag.String("gui", "./P2PTogether", "Path to P2PTogether executable")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Create libp2p host
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", *port)),
	)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Test Agent started. Peer ID: %s. Listening on: %v", h.ID(), h.Addrs())

	agent := &Agent{
		Host:           h,
		Ctx:            ctx,
		Cancel:         cancel,
		GUIPort:        GuiTestPort,
		DefaultBinPath: *guiBin,
	}

	// 2. Setup mDNS discovery (Advertising)
	if err := setupMDNS(h, MdnsServiceTag); err != nil {
		log.Fatal("Failed to setup mDNS:", err)
	}
	log.Println("mDNS advertising started.")

	// 3. Set stream handler for orchestrator commands
	h.SetStreamHandler(AgentProtocolID, agent.handleStream)

	// 4. Wait for shutdown signal
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Println("Shutting down...")

	agent.StopGUI()
	h.Close()
}

func setupMDNS(h host.Host, serviceTag string) error {
	s := mdns.NewMdnsService(h, serviceTag, &noopNotifee{})
	return s.Start()
}

type noopNotifee struct{}

func (n *noopNotifee) HandlePeerFound(pi peer.AddrInfo) {}

// handleStream handles commands from the orchestrator
func (a *Agent) handleStream(s network.Stream) {
	log.Printf("New stream from %s", s.Conn().RemotePeer())
	defer s.Close()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	for {
		// Read command (JSON Line)
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Println("Stream closed or error:", err)
			return
		}

		log.Printf("Received command: %s", line)
		response := a.processCommand(line)

		// Send response
		writer.WriteString(response + "\n")
		writer.Flush()
	}
}

type Command struct {
	Action string            `json:"action"`
	Args   map[string]string `json:"args"`
}

type Response struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Data   string `json:"data,omitempty"`
}

func (a *Agent) processCommand(jsonCmd string) string {
	var cmd Command
	if err := json.Unmarshal([]byte(jsonCmd), &cmd); err != nil {
		return errorResponse("Invalid JSON")
	}

	var err error
	switch cmd.Action {
	case "start_gui":
		err = a.StartGUI(cmd.Args["bin_path"])
	case "stop_gui":
		err = a.StopGUI()
	case "gui_command":
		err = a.SendGUICommand(cmd.Args["payload"])
	case "ping":
		return successResponse("pong")
	default:
		return errorResponse("Unknown action: " + cmd.Action)
	}

	if err != nil {
		return errorResponse(err.Error())
	}
	return successResponse("OK")
}

func (a *Agent) StartGUI(binPath string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.GUIProcess != nil {
		return fmt.Errorf("GUI already running")
	}

	if binPath == "" {
		binPath = a.DefaultBinPath
	}

	// Check if binPath exists, if not try to be smart if it starts with ./
	if _, err := os.Stat(binPath); os.IsNotExist(err) && binPath == "./P2PTogether" {
		// Try common build location
		candidates := []string{
			"build/bin/P2PTogether",
			"build/P2PTogether",
			"bin/P2PTogether",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				binPath = c
				break
			}
		}
	}

	// Resolve absolute path if possible
	if abs, err := filepath.Abs(binPath); err == nil {
		binPath = abs
	}

	cmd := exec.Command(binPath,
		"--test-mode",
		fmt.Sprintf("--test-port=%d", a.GUIPort),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("Starting GUI: %s --test-mode --test-port=%d", binPath, a.GUIPort)
	if err := cmd.Start(); err != nil {
		return err
	}

	a.GUIProcess = cmd

	// Give it a moment to start listening
	time.Sleep(2 * time.Second)

	return nil
}

func (a *Agent) StopGUI() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.GUIProcess == nil {
		return nil
	}

	log.Println("Stopping GUI...")
	if err := a.GUIProcess.Process.Signal(syscall.SIGTERM); err != nil {
		// Fallback to Kill
		a.GUIProcess.Process.Kill()
	}
	// Wait releases resources
	a.GUIProcess.Wait()
	a.GUIProcess = nil
	return nil
}

func (a *Agent) SendGUICommand(payload string) error {
	// Connect to GUI TCP port with retries
	var conn net.Conn
	var err error
	for i := 0; i < 10; i++ {
		conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", a.GUIPort))
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to GUI test server after retries: %v", err)
	}
	defer conn.Close()

	log.Printf("Sending to GUI: %s", payload)
	_, err = fmt.Fprintf(conn, "%s\n", payload)
	return err
}

func successResponse(data string) string {
	r := Response{Status: "ok", Data: data}
	b, _ := json.Marshal(r)
	return string(b)
}

func errorResponse(msg string) string {
	r := Response{Status: "error", Error: msg}
	b, _ := json.Marshal(r)
	return string(b)
}
