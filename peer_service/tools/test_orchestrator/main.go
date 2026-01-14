package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	AgentProtocolID = "/p2ptogether-test/agent/1.0.0"
	MdnsServiceTag  = "_p2ptogether-test._tcp"
	DHTRendezvous   = "p2ptogether-test-agents"
)

type Orchestrator struct {
	Host         host.Host
	Agents       map[string]peer.AddrInfo // Map Agent ID (or role?) to PeerInfo
	AgentStreams map[string]network.Stream
	Variables    map[string]string // Captured variables for substitution
	Ctx          context.Context
	mu           sync.Mutex
}

func main() {
	scenarioFile := flag.String("scenario", "", "Path to scenario file (JSON for now)")
	expectedAgents := flag.Int("agents", 1, "Number of agents to wait for")
	useDHT := flag.Bool("dht", false, "Enable DHT-based discovery (for networks that block mDNS)")
	noMdns := flag.Bool("no-mdns", false, "Disable mDNS discovery (useful for testing DHT-only)")
	flag.Parse()

	if *scenarioFile == "" {
		log.Fatal("Please provide -scenario file")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("=== Test Orchestrator Starting ===")
	log.Printf("Configuration: scenario=%s, agents=%d, dht=%v, no-mdns=%v", *scenarioFile, *expectedAgents, *useDHT, *noMdns)

	// 1. Create Host
	h, err := libp2p.New()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Orchestrator started. Peer ID: %s", h.ID())
	log.Printf("Listening addresses:")
	for _, addr := range h.Addrs() {
		log.Printf("  - %s/p2p/%s", addr, h.ID())
	}

	orch := &Orchestrator{
		Host:         h,
		Ctx:          ctx,
		Agents:       make(map[string]peer.AddrInfo),
		AgentStreams: make(map[string]network.Stream),
		Variables:    make(map[string]string),
	}

	// 2. Setup mDNS Discovery - unless disabled
	if !*noMdns {
		if err := setupMDNS(h, MdnsServiceTag, orch); err != nil {
			log.Printf("[mDNS] Warning: setup failed: %v (continuing without mDNS)", err)
		} else {
			log.Println("[mDNS] Discovery started on service tag:", MdnsServiceTag)
		}
	} else {
		log.Println("[mDNS] Disabled via --no-mdns flag")
	}

	// 3. Setup DHT Discovery if enabled
	if *useDHT {
		if err := setupDHT(ctx, h, orch); err != nil {
			log.Printf("[DHT] Warning: setup failed: %v", err)
		}
	} else {
		log.Println("[DHT] Disabled (use --dht to enable)")
	}

	// 4. Wait for agents
	log.Printf("=== Waiting for %d agent(s) ===", *expectedAgents)
	startTime := time.Now()
	lastCount := 0
	for {
		orch.mu.Lock()
		count := len(orch.Agents)
		orch.mu.Unlock()

		if count != lastCount {
			log.Printf("[Discovery] Progress: %d/%d agents found (elapsed: %v)", count, *expectedAgents, time.Since(startTime).Round(time.Second))
			lastCount = count
		}

		if count >= *expectedAgents {
			break
		}
		time.Sleep(1 * time.Second)
	}
	log.Printf("=== All %d agent(s) discovered in %v ===", *expectedAgents, time.Since(startTime).Round(time.Second))

	// 5. Connect to agents
	log.Println("Connecting to discovered agents...")
	for id, pi := range orch.Agents {
		shortID := id[:16]
		log.Printf("[Connect] Attempting connection to agent %s...", shortID)
		// Sort addresses to prioritize local/private ones (fixes NAT hairpinning issues)
		sortedAddrs := sortAddressesLocalFirst(pi.Addrs)
		pi.Addrs = sortedAddrs
		log.Printf("[Connect] Agent addresses (sorted): %v", pi.Addrs)

		connectStart := time.Now()
		if err := h.Connect(ctx, pi); err != nil {
			log.Printf("[Connect] Failed to connect to agent %s: %v", shortID, err)
			continue
		}
		log.Printf("[Connect] Connected to agent %s in %v", shortID, time.Since(connectStart).Round(time.Millisecond))

		s, err := h.NewStream(ctx, pi.ID, AgentProtocolID)
		if err != nil {
			log.Printf("[Connect] Failed to open stream to agent %s: %v", shortID, err)
			continue
		}
		orch.AgentStreams[id] = s
		log.Printf("[Connect] Stream opened to agent %s", shortID)
	}

	log.Printf("=== Connected to %d/%d agents ===", len(orch.AgentStreams), len(orch.Agents))

	// 6. Load and Run Scenario
	log.Printf("Loading scenario: %s", *scenarioFile)
	if err := orch.RunScenario(*scenarioFile); err != nil {
		log.Fatal("Scenario failed:", err)
	}

	log.Println("=== Test Scenario Completed Successfully ===")
}

func setupMDNS(h host.Host, tag string, notifee mdns.Notifee) error {
	s := mdns.NewMdnsService(h, tag, notifee)
	return s.Start()
}

func setupDHT(ctx context.Context, h host.Host, orch *Orchestrator) error {
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

	// Start discovering agents via DHT in background
	routingDiscovery := drouting.NewRoutingDiscovery(kademliaDHT)
	go func() {
		log.Printf("[DHT] Starting peer discovery on rendezvous: %s", DHTRendezvous)
		discoveryAttempt := 0
		for {
			select {
			case <-ctx.Done():
				log.Println("[DHT] Discovery stopped - context cancelled")
				return
			default:
			}

			discoveryAttempt++
			log.Printf("[DHT] Discovery attempt #%d...", discoveryAttempt)

			findCtx, findCancel := context.WithTimeout(ctx, 30*time.Second)
			peerChan, err := routingDiscovery.FindPeers(findCtx, DHTRendezvous)
			if err != nil {
				log.Printf("[DHT] FindPeers error: %v", err)
				findCancel()
				time.Sleep(5 * time.Second)
				continue
			}

			foundCount := 0
			for pi := range peerChan {
				if pi.ID == h.ID() || pi.ID == "" {
					continue // Skip self and empty
				}
				foundCount++
				log.Printf("[DHT] Found peer: %s with addresses: %v", pi.ID.String()[:16], pi.Addrs)
				orch.HandlePeerFound(pi)
			}
			findCancel()

			if foundCount > 0 {
				log.Printf("[DHT] Discovery attempt #%d found %d peer(s)", discoveryAttempt, foundCount)
			}

			time.Sleep(5 * time.Second) // Re-check periodically
		}
	}()

	log.Println("[DHT] Discovery started in background")
	return nil
}

func (o *Orchestrator) HandlePeerFound(pi peer.AddrInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := pi.ID.String()
	if _, exists := o.Agents[id]; !exists {
		log.Printf("[Discovery] New agent found: %s", id[:16])
		log.Printf("[Discovery] Agent addresses: %v", pi.Addrs)
		o.Agents[id] = pi
	}
}

// Simple Scenario Structs (JSON)
type Scenario struct {
	Steps []Step `json:"steps"`
}

type Step struct {
	DelayMs      int64             `json:"delay_ms"`      // Delay from previous step completion in ms
	AgentIndices []int             `json:"agent_indices"` // 0-based indices in discovered list (commands sent in parallel)
	Action       string            `json:"action"`
	Args         map[string]string `json:"args"`
	CaptureAs    string            `json:"capture_as,omitempty"` // Variable name to store response data (only from first agent)
}

func (o *Orchestrator) RunScenario(path string) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	var scenario Scenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		return fmt.Errorf("failed to parse scenario: %v", err)
	}

	// Get list of agent IDs to map indices
	o.mu.Lock()
	agentIDs := make([]string, 0, len(o.AgentStreams))
	for id := range o.AgentStreams {
		agentIDs = append(agentIDs, id)
	}
	o.mu.Unlock()

	log.Printf("[Scenario] Starting with %d steps across %d agent(s)", len(scenario.Steps), len(agentIDs))

	for stepNum, step := range scenario.Steps {
		// Wait relative to previous step
		if step.DelayMs > 0 {
			log.Printf("[Scenario] Step %d: waiting %dms...", stepNum, step.DelayMs)
			time.Sleep(time.Duration(step.DelayMs) * time.Millisecond)
		}

		if len(step.AgentIndices) == 0 {
			log.Printf("[Scenario] Skipping step %d: No agent indices specified", stepNum)
			continue
		}

		log.Printf("[Scenario] Step %d: action=%s, agents=%v", stepNum, step.Action, step.AgentIndices)

		// Execute commands to all targeted agents in parallel
		var wg sync.WaitGroup
		errChan := make(chan error, len(step.AgentIndices))

		for i, agentIdx := range step.AgentIndices {
			if agentIdx >= len(agentIDs) {
				log.Printf("[Scenario] Skipping agent index %d in step %d: out of range", agentIdx, stepNum)
				continue
			}

			wg.Add(1)
			// Only capture from the first agent in the list
			captureAs := ""
			if i == 0 {
				captureAs = step.CaptureAs
			}

			go func(agentID string, captureVar string) {
				defer wg.Done()
				if err := o.SendCommand(agentID, step.Action, step.Args, captureVar); err != nil {
					errChan <- fmt.Errorf("agent %s: %v", agentID[:16], err)
				}
			}(agentIDs[agentIdx], captureAs)
		}

		wg.Wait()
		close(errChan)

		// Collect any errors
		var errs []string
		for err := range errChan {
			errs = append(errs, err.Error())
		}
		if len(errs) > 0 {
			return fmt.Errorf("step %d failed: %s", stepNum, strings.Join(errs, "; "))
		}

		log.Printf("[Scenario] Step %d completed successfully", stepNum)
	}

	return nil
}

// substituteVariables replaces ${varName} patterns in args with values from Variables map
func (o *Orchestrator) substituteVariables(args map[string]string) map[string]string {
	result := make(map[string]string)
	varPattern := regexp.MustCompile(`\$\{([^}]+)\}`)

	for k, v := range args {
		result[k] = varPattern.ReplaceAllStringFunc(v, func(match string) string {
			varName := varPattern.FindStringSubmatch(match)[1]
			if val, ok := o.Variables[varName]; ok {
				return val
			}
			log.Printf("[Variables] Warning: ${%s} not found, keeping as-is", varName)
			return match
		})
	}
	return result
}

func (o *Orchestrator) SendCommand(agentID string, action string, args map[string]string, captureAs string) error {
	shortID := agentID[:16]

	// Substitute variables in args
	substitutedArgs := o.substituteVariables(args)

	cmd := map[string]interface{}{
		"action": action,
		"args":   substitutedArgs,
	}
	bytes, _ := json.Marshal(cmd)

	// Try to send command, reconnect if stream is stale
	for attempt := 0; attempt < 2; attempt++ {
		s, ok := o.AgentStreams[agentID]
		if !ok {
			// Try to reconnect
			log.Printf("[Command] No stream for agent %s, attempting reconnect...", shortID)
			if err := o.reconnectAgent(agentID); err != nil {
				return fmt.Errorf("no stream for agent %s and reconnect failed: %v", shortID, err)
			}
			s = o.AgentStreams[agentID]
		}

		log.Printf("[Command] Sending to %s: %s", shortID, string(bytes))
		_, err := s.Write(bytes)
		if err != nil {
			log.Printf("[Command] Write failed to %s (attempt %d): %v", shortID, attempt+1, err)
			s.Close()
			delete(o.AgentStreams, agentID)
			continue
		}
		_, err = s.Write([]byte("\n"))
		if err != nil {
			log.Printf("[Command] Write newline failed to %s (attempt %d): %v", shortID, attempt+1, err)
			s.Close()
			delete(o.AgentStreams, agentID)
			continue
		}

		// Read response
		reader := bufio.NewReader(s)
		respLine, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("[Command] Read failed from %s (attempt %d): %v", shortID, attempt+1, err)
			s.Close()
			delete(o.AgentStreams, agentID)
			continue
		}
		log.Printf("[Command] Response from %s: %s", shortID, strings.TrimSpace(respLine))

		// Capture data if requested
		if captureAs != "" {
			var resp map[string]interface{}
			if err := json.Unmarshal([]byte(respLine), &resp); err == nil {
				if data, ok := resp["data"].(string); ok {
					// Strip known label prefixes (e.g., "Invite Code: ABC123" -> "ABC123")
					data = strings.TrimPrefix(data, "Invite Code: ")
					data = strings.TrimPrefix(data, "Session ID: ")
					data = strings.TrimSpace(data)
					o.Variables[captureAs] = data
					log.Printf("[Variables] Captured %s = %s", captureAs, data)
				}
			}
		}

		return nil
	}

	return fmt.Errorf("failed to communicate with agent %s after retries", shortID)
}

func (o *Orchestrator) reconnectAgent(agentID string) error {
	shortID := agentID[:16]

	o.mu.Lock()
	pi, ok := o.Agents[agentID]
	o.mu.Unlock()
	if !ok {
		return fmt.Errorf("agent %s not found", shortID)
	}

	log.Printf("[Reconnect] Attempting to reconnect to agent %s...", shortID)
	log.Printf("[Reconnect] Agent addresses: %v", pi.Addrs)

	connectStart := time.Now()
	if err := o.Host.Connect(o.Ctx, pi); err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	log.Printf("[Reconnect] Connected in %v", time.Since(connectStart).Round(time.Millisecond))

	s, err := o.Host.NewStream(o.Ctx, pi.ID, AgentProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream: %v", err)
	}

	o.AgentStreams[agentID] = s
	log.Printf("[Reconnect] Stream reopened to agent %s", shortID)
	return nil
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
