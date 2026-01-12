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
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

const (
	AgentProtocolID = "/p2ptogether-test/agent/1.0.0"
	MdnsServiceTag  = "_p2ptogether-test._tcp"
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
		Variables:    make(map[string]string),
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

	log.Printf("Starting scenario with %d steps across %d agents", len(scenario.Steps), len(agentIDs))

	for stepNum, step := range scenario.Steps {
		// Wait relative to previous step
		time.Sleep(time.Duration(step.DelayMs) * time.Millisecond)

		if len(step.AgentIndices) == 0 {
			log.Printf("Skipping step %d: No agent indices specified", stepNum)
			continue
		}

		// Execute commands to all targeted agents in parallel
		var wg sync.WaitGroup
		errChan := make(chan error, len(step.AgentIndices))

		for i, agentIdx := range step.AgentIndices {
			if agentIdx >= len(agentIDs) {
				log.Printf("Skipping agent index %d in step %d: out of range", agentIdx, stepNum)
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
					errChan <- fmt.Errorf("agent %s: %v", agentID, err)
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
			log.Printf("Warning: variable ${%s} not found, keeping as-is", varName)
			return match
		})
	}
	return result
}

func (o *Orchestrator) SendCommand(agentID string, action string, args map[string]string, captureAs string) error {
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
			if err := o.reconnectAgent(agentID); err != nil {
				return fmt.Errorf("no stream for agent %s and reconnect failed: %v", agentID, err)
			}
			s = o.AgentStreams[agentID]
		}

		log.Printf("Sending to %s: %s", agentID, string(bytes))
		_, err := s.Write(bytes)
		if err != nil {
			log.Printf("Write failed to %s (attempt %d): %v - retrying with fresh stream", agentID, attempt+1, err)
			s.Close()
			delete(o.AgentStreams, agentID)
			continue
		}
		_, err = s.Write([]byte("\n"))
		if err != nil {
			log.Printf("Write newline failed to %s (attempt %d): %v - retrying with fresh stream", agentID, attempt+1, err)
			s.Close()
			delete(o.AgentStreams, agentID)
			continue
		}

		// Read response
		reader := bufio.NewReader(s)
		respLine, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("Read failed from %s (attempt %d): %v - retrying with fresh stream", agentID, attempt+1, err)
			s.Close()
			delete(o.AgentStreams, agentID)
			continue
		}
		log.Printf("Response from %s: %s", agentID, respLine)

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
					log.Printf("Captured variable %s = %s", captureAs, data)
				}
			}
		}

		return nil
	}

	return fmt.Errorf("failed to communicate with agent %s after retries", agentID)
}

func (o *Orchestrator) reconnectAgent(agentID string) error {
	o.mu.Lock()
	pi, ok := o.Agents[agentID]
	o.mu.Unlock()
	if !ok {
		return fmt.Errorf("agent %s not found", agentID)
	}

	log.Printf("Reconnecting to agent %s...", agentID)

	if err := o.Host.Connect(o.Ctx, pi); err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}

	s, err := o.Host.NewStream(o.Ctx, pi.ID, AgentProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream: %v", err)
	}

	o.AgentStreams[agentID] = s
	log.Printf("Reconnected to agent %s", agentID)
	return nil
}
