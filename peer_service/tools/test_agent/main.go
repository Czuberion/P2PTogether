package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	AgentProtocolID    = "/p2ptogether-test/agent/1.0.0"
	MdnsServiceTag     = "_p2ptogether-test._tcp"
	DHTRendezvous      = "p2ptogether-test-agents"
	DefaultGuiTestPort = 19999
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
	useDHT := flag.Bool("dht", false, "Enable DHT-based discovery (for networks that block mDNS)")
	noMdns := flag.Bool("no-mdns", false, "Disable mDNS discovery (useful for testing DHT-only)")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("=== Test Agent Starting ===")
	log.Printf("Configuration: port=%d, gui=%s, dht=%v, no-mdns=%v", *port, *guiBin, *useDHT, *noMdns)

	// 1. Create libp2p host
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", *port)),
	)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Test Agent started. Peer ID: %s", h.ID())
	log.Printf("Listening addresses:")
	for _, addr := range h.Addrs() {
		log.Printf("  - %s/p2p/%s", addr, h.ID())
	}

	agent := &Agent{
		Host:           h,
		Ctx:            ctx,
		Cancel:         cancel,
		GUIPort:        0, // Will be auto-detected when GUI starts
		DefaultBinPath: *guiBin,
	}

	// 2. Setup mDNS discovery (Advertising) - unless disabled
	if !*noMdns {
		if err := setupMDNS(h, MdnsServiceTag); err != nil {
			log.Printf("[mDNS] Warning: setup failed: %v (continuing without mDNS)", err)
		} else {
			log.Println("[mDNS] Advertising started on service tag:", MdnsServiceTag)
		}
	} else {
		log.Println("[mDNS] Disabled via --no-mdns flag")
	}

	// 3. Setup DHT discovery if enabled
	if *useDHT {
		if err := setupDHT(ctx, h); err != nil {
			log.Printf("[DHT] Warning: setup failed: %v", err)
		}
	} else {
		log.Println("[DHT] Disabled (use --dht to enable)")
	}

	// 4. Set stream handler for orchestrator commands
	h.SetStreamHandler(AgentProtocolID, agent.handleStream)
	log.Printf("Stream handler registered for protocol: %s", AgentProtocolID)

	log.Println("=== Agent Ready - Waiting for Orchestrator ===")

	// 5. Wait for shutdown signal
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

func setupDHT(ctx context.Context, h host.Host) error {
	log.Println("[DHT] Initializing Kademlia DHT...")

	// Create DHT in server mode
	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return fmt.Errorf("failed to create DHT: %v", err)
	}

	// Connect to bootstrap peers
	log.Println("[DHT] Connecting to bootstrap peers...")
	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	totalPeers := len(dht.DefaultBootstrapPeers)

	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerInfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			log.Printf("[DHT] Failed to parse bootstrap peer %s: %v", peerAddr, err)
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			// Sort addresses to prioritize local private ones (fixes NAT hairpinning)
			pi.Addrs = sortAddressesLocalFirst(pi.Addrs)

			if err := h.Connect(connectCtx, pi); err != nil {
				log.Printf("[DHT] Failed to connect to bootstrap peer %s: %v", pi.ID.String()[:16], err)
			} else {
				mu.Lock()
				successCount++
				mu.Unlock()
				log.Printf("[DHT] Connected to bootstrap peer: %s", pi.ID.String()[:16])
			}
		}(*peerInfo)
	}
	wg.Wait()
	log.Printf("[DHT] Bootstrap connections: %d/%d successful", successCount, totalPeers)

	if successCount == 0 {
		return fmt.Errorf("failed to connect to any bootstrap peers")
	}

	// Bootstrap the DHT
	log.Println("[DHT] Bootstrapping routing table...")
	if err := kademliaDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("DHT bootstrap failed: %v", err)
	}

	// Wait for routing table to populate
	log.Println("[DHT] Waiting for routing table to populate...")
	time.Sleep(3 * time.Second)
	routingTableSize := kademliaDHT.RoutingTable().Size()
	log.Printf("[DHT] Routing table size: %d peers", routingTableSize)

	// Advertise ourselves
	routingDiscovery := drouting.NewRoutingDiscovery(kademliaDHT)
	log.Printf("[DHT] Advertising on rendezvous: %s", DHTRendezvous)
	dutil.Advertise(ctx, routingDiscovery, DHTRendezvous)
	log.Println("[DHT] Advertisement started - agent is now discoverable via DHT")

	return nil
}

type noopNotifee struct{}

func (n *noopNotifee) HandlePeerFound(pi peer.AddrInfo) {}

// handleStream handles commands from the orchestrator
func (a *Agent) handleStream(s network.Stream) {
	remotePeer := s.Conn().RemotePeer()
	log.Printf("[Stream] New connection from orchestrator: %s", remotePeer.String()[:16])
	log.Printf("[Stream] Remote address: %s", s.Conn().RemoteMultiaddr())
	defer func() {
		s.Close()
		log.Printf("[Stream] Connection closed from: %s", remotePeer.String()[:16])
	}()

	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)

	for {
		// Read command (JSON Line)
		line, err := reader.ReadString('\n')
		if err != nil {
			if err.Error() != "EOF" {
				log.Printf("[Stream] Read error: %v", err)
			}
			return
		}

		log.Printf("[Command] Received: %s", strings.TrimSpace(line))
		response := a.processCommand(line)

		// Send response
		writer.WriteString(response + "\n")
		writer.Flush()
		log.Printf("[Command] Response sent: %s", strings.TrimSpace(response))
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
		guiResponse, guiErr := a.SendGUICommand(cmd.Args["payload"])
		if guiErr != nil {
			return errorResponse(guiErr.Error())
		}
		// Forward data from GUI response if present
		var guiResp map[string]interface{}
		if json.Unmarshal([]byte(guiResponse), &guiResp) == nil {
			if data, ok := guiResp["data"].(string); ok {
				return successResponseWithData(data)
			}
		}
		return successResponse("OK")
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

	// Start with port 0 to let OS pick a free port
	cmd := exec.Command(binPath,
		"--test-mode",
		"--test-port=0",
	)

	// Capture stdout and stderr to parse for actual port (Qt logs to stderr via qInfo)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	log.Printf("[GUI] Starting: %s --test-mode --test-port=0", binPath)
	if err := cmd.Start(); err != nil {
		return err
	}

	a.GUIProcess = cmd

	// Parse both stdout and stderr for the actual port, with timeout
	portChan := make(chan int, 1)
	portPattern := regexp.MustCompile(`Test server listening on port (\d+)`)

	// Helper to scan a reader for port
	scanForPort := func(reader io.Reader, name string) {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Printf("[GUI/%s] %s\n", name, line) // Echo to console with label
			if matches := portPattern.FindStringSubmatch(line); len(matches) > 1 {
				if port, err := strconv.Atoi(matches[1]); err == nil {
					select {
					case portChan <- port:
					default: // Already found
					}
				}
			}
		}
	}

	go scanForPort(stdout, "stdout")
	go scanForPort(stderr, "stderr")

	// Wait for port discovery with timeout
	select {
	case port := <-portChan:
		a.GUIPort = port
		log.Printf("[GUI] Test server discovered on port %d", port)
	case <-time.After(30 * time.Second):
		log.Printf("[GUI] Warning: Could not discover test port, using default %d", DefaultGuiTestPort)
		a.GUIPort = DefaultGuiTestPort
	}

	return nil
}

func (a *Agent) StopGUI() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.GUIProcess == nil {
		return nil
	}

	log.Println("[GUI] Stopping...")
	if err := a.GUIProcess.Process.Signal(syscall.SIGTERM); err != nil {
		// Fallback to Kill
		a.GUIProcess.Process.Kill()
	}
	// Wait releases resources
	a.GUIProcess.Wait()
	a.GUIProcess = nil
	log.Println("[GUI] Stopped")
	return nil
}

func (a *Agent) SendGUICommand(payload string) (string, error) {
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
		return "", fmt.Errorf("failed to connect to GUI test server after retries: %v", err)
	}
	defer conn.Close()

	log.Printf("[GUI] Sending command: %s", payload)
	_, err = fmt.Fprintf(conn, "%s\n", payload)
	if err != nil {
		return "", err
	}

	// Determine timeout based on command type
	// For wait_for_event commands, use the timeout from the payload (or a generous default)
	readTimeout := 2 * time.Second // Default for most commands

	var payloadObj map[string]interface{}
	if json.Unmarshal([]byte(payload), &payloadObj) == nil {
		if action, ok := payloadObj["action"].(string); ok && action == "wait_for_event" {
			// Extract timeout from args if present
			if args, ok := payloadObj["args"].(map[string]interface{}); ok {
				if timeoutMs, ok := args["timeout_ms"].(float64); ok && timeoutMs > 0 {
					// Add 5 seconds buffer to the GUI timeout
					readTimeout = time.Duration(timeoutMs+5000) * time.Millisecond
				} else {
					// Default wait_for_event timeout: 2 minutes + buffer
					readTimeout = 125 * time.Second
				}
			} else {
				readTimeout = 125 * time.Second
			}
			log.Printf("[GUI] wait_for_event detected, using timeout: %v", readTimeout)
		}
	}

	// Read response from GUI with timeout
	// Some commands (like trigger_menu opening modal dialogs) won't respond until dialog closes
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	reader := bufio.NewReader(conn)
	respLine, err := reader.ReadString('\n')
	if err != nil {
		// Timeout or EOF is expected for commands that trigger modal dialogs (except wait_for_event)
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			log.Printf("[GUI] Read timeout (expected for modal dialogs)")
			return "", nil
		}
		log.Printf("[GUI] Warning: no response: %v", err)
		return "", nil // Not an error - GUI might not respond
	}
	log.Printf("[GUI] Response: %s", strings.TrimSpace(respLine))
	return strings.TrimSpace(respLine), nil
}

func successResponse(data string) string {
	r := Response{Status: "ok", Data: data}
	b, _ := json.Marshal(r)
	return string(b)
}

func successResponseWithData(data string) string {
	r := Response{Status: "ok", Data: data}
	b, _ := json.Marshal(r)
	return string(b)
}

func errorResponse(msg string) string {
	r := Response{Status: "error", Error: msg}
	b, _ := json.Marshal(r)
	return string(b)
}

// Helper to prioritize local/private addresses
func sortAddressesLocalFirst(addrs []ma.Multiaddr) []ma.Multiaddr {
	// We want to try local addresses first, then public ones.
	// This helps when both peers are on the same LAN behind a NAT that doesn't support hairpinning.
	var local, public []ma.Multiaddr

	for _, addr := range addrs {
		if manet.IsPrivateAddr(addr) {
			local = append(local, addr)
		} else {
			public = append(public, addr)
		}
	}

	// Combine local + public
	return append(local, public...)
}
