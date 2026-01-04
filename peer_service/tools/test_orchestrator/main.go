package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	// "gopkg.in/yaml.v3" // Commented out until verify dependency
)

const (
	AgentProtocolID = "/p2ptogether-test/agent/1.0.0"
	MdnsServiceTag  = "_p2ptogether-test._tcp"
)

type Orchestrator struct {
	Host         host.Host
	Agents       map[string]peer.AddrInfo // Map Agent ID (or role?) to PeerInfo
	AgentStreams map[string]network.Stream
	Ctx          context.Context
	mu           sync.Mutex
}

func main() {
	scenarioFile := flag.String("scenario", "", "Path to scenario file (JSON for now)")
	// Wait for N agents?
	expectedAgents := flag.Int("agents", 1, "Number of agents to wait for")
	flag.Parse()

	if *scenarioFile == "" {
		log.Fatal("Please provide -scenario file")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Create Host
	h, err := libp2p.New()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Orchestrator started. Peer ID: %s", h.ID())

	orch := &Orchestrator{
		Host:         h,
		Ctx:          ctx,
		Agents:       make(map[string]peer.AddrInfo),
		AgentStreams: make(map[string]network.Stream),
	}

	// 2. Setup mDNS Discovery
	if err := setupMDNS(h, MdnsServiceTag, orch); err != nil {
		log.Fatal(err)
	}

	// 3. Wait for agents
	log.Printf("Waiting for %d agents...", *expectedAgents)
	for {
		orch.mu.Lock()
		count := len(orch.Agents)
		orch.mu.Unlock()
		if count >= *expectedAgents {
			break
		}
		time.Sleep(1 * time.Second)
	}
	log.Println("All agents discovered! Connecting...")

	// 4. Connect to agents
	for id, pi := range orch.Agents {
		if err := h.Connect(ctx, pi); err != nil {
			log.Printf("Failed to connect to agent %s: %v", id, err)
			continue
		}

		s, err := h.NewStream(ctx, pi.ID, AgentProtocolID)
		if err != nil {
			log.Printf("Failed to open stream to agent %s: %v", id, err)
			continue
		}
		orch.AgentStreams[id] = s
		log.Printf("Connected to agent %s", id)
	}

	// 5. Load and Run Scenario
	if err := orch.RunScenario(*scenarioFile); err != nil {
		log.Fatal("Scenario failed:", err)
	}

	log.Println("Test Scenario Completed Successfully.")
}

func setupMDNS(h host.Host, tag string, notifee mdns.Notifee) error {
	s := mdns.NewMdnsService(h, tag, notifee)
	return s.Start()
}

func (o *Orchestrator) HandlePeerFound(pi peer.AddrInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// In real implementation, we might want to handshake to identify which agent is on which machine
	// For now, key by Peer ID string
	id := pi.ID.String()
	if _, exists := o.Agents[id]; !exists {
		log.Printf("Discovered agent: %s", id)
		o.Agents[id] = pi
	}
}

// Simple Scenario Structs (JSON)
type Scenario struct {
	Steps []Step `json:"steps"`
}

type Step struct {
	DelayMs    int64             `json:"delay_ms"`    // Delay from previous step completion in ms
	AgentIndex int               `json:"agent_index"` // 0-based index in discovered list
	Action     string            `json:"action"`
	Args       map[string]string `json:"args"`
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

	log.Printf("Starting scenario with %d steps across %d agents", len(scenario.Steps), len(agentIDs))

	for _, step := range scenario.Steps {
		// Wait relative to previous step
		time.Sleep(time.Duration(step.DelayMs) * time.Millisecond)

		if step.AgentIndex >= len(agentIDs) {
			log.Printf("Skipping step: Agent index %d out of range", step.AgentIndex)
			continue
		}
		agentID := agentIDs[step.AgentIndex]

		// Execute step
		if err := o.SendCommand(agentID, step.Action, step.Args); err != nil {
			return fmt.Errorf("step failed: %v", err)
		}
	}

	return nil
}

func (o *Orchestrator) SendCommand(agentID string, action string, args map[string]string) error {
	s, ok := o.AgentStreams[agentID]
	if !ok {
		return fmt.Errorf("no stream for agent %s", agentID)
	}

	cmd := map[string]interface{}{
		"action": action,
		"args":   args,
	}
	bytes, _ := json.Marshal(cmd)

	log.Printf("Sending to %s: %s", agentID, string(bytes))
	s.Write(bytes)
	s.Write([]byte("\n"))

	// Read response
	reader := bufio.NewReader(s)
	respLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	log.Printf("Response from %s: %s", agentID, respLine)

	return nil
}
