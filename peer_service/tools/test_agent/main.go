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
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

const (
	AgentProtocolID    = "/p2ptogether-test/agent/1.0.0"
	MdnsServiceTag     = "_p2ptogether-test._tcp"
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
		GUIPort:        0, // Will be auto-detected when GUI starts
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

	log.Printf("Starting GUI: %s --test-mode --test-port=0", binPath)
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
			fmt.Printf("[%s] %s\n", name, line) // Echo to console with label
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
		log.Printf("Discovered GUI test server on port %d", port)
	case <-time.After(30 * time.Second):
		log.Printf("Warning: Could not discover GUI test port, using default %d", DefaultGuiTestPort)
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

	log.Printf("Sending to GUI: %s", payload)
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
			log.Printf("wait_for_event detected, using timeout: %v", readTimeout)
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
			log.Printf("Read timeout (expected for modal dialogs)")
			return "", nil
		}
		log.Printf("Warning: no response from GUI: %v", err)
		return "", nil // Not an error - GUI might not respond
	}
	log.Printf("Response from GUI: %s", respLine)
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
