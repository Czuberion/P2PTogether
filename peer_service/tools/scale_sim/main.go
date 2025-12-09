package main

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// MetricsCollector aggregates latency measurements.
type MetricsCollector struct {
	mu        sync.Mutex
	latencies []time.Duration
	csvPath   string
	startTime time.Time
}

func NewMetricsCollector(csvPath string) *MetricsCollector {
	return &MetricsCollector{
		csvPath:   csvPath,
		startTime: time.Now(),
		latencies: make([]time.Duration, 0, 10000),
	}
}

func (mc *MetricsCollector) Add(d time.Duration) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.latencies = append(mc.latencies, d)
}

func (mc *MetricsCollector) SaveCSV() error {
	if mc.csvPath == "" {
		return nil
	}
	f, err := os.Create(mc.csvPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	w.Write([]string{"latency_ms"})
	mc.mu.Lock()
	defer mc.mu.Unlock()
	for _, l := range mc.latencies {
		w.Write([]string{fmt.Sprintf("%.4f", float64(l.Microseconds())/1000.0)})
	}
	return nil
}

func (mc *MetricsCollector) Report() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	count := len(mc.latencies)
	if count == 0 {
		log.Println("No metrics collected.")
		return
	}

	// Sort for percentiles
	sorted := make([]time.Duration, count)
	copy(sorted, mc.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	min := sorted[0]
	max := sorted[count-1]

	total := int64(0)
	for _, l := range sorted {
		total += int64(l)
	}
	avg := time.Duration(total / int64(count))

	p50 := sorted[int(math.Max(0, float64(count)*0.50-1))]
	p95 := sorted[int(math.Max(0, float64(count)*0.95-1))]
	p99 := sorted[int(math.Max(0, float64(count)*0.99-1))]

	fmt.Println("\n=== Scalability Test Results ===")
	fmt.Printf("Total Messages Received: %d\n", count)
	fmt.Printf("Min Latency: %v\n", min)
	fmt.Printf("Max Latency: %v\n", max)
	fmt.Printf("Avg Latency: %v\n", avg)
	fmt.Printf("P50 Latency: %v\n", p50)
	fmt.Printf("P95 Latency: %v\n", p95)
	fmt.Printf("P99 Latency: %v\n", p99)
	fmt.Println("================================")

	if mc.csvPath != "" {
		fmt.Printf("Raw data saved to %s\n", mc.csvPath)
	}
}

func main() {
	numPeers := flag.Int("n", 5, "Number of peers")
	topicName := flag.String("topic", "p2ptogether-bench", "GossipSub topic name")
	duration := flag.Duration("d", 10*time.Second, "Test duration")
	csvFile := flag.String("csv", "", "Path to save CSV data (optional)")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collector := NewMetricsCollector(*csvFile)

	log.Printf("Starting Scalability Sim with %d peers on topic %s...", *numPeers, *topicName)

	peers := make([]host.Host, *numPeers)
	addrs := make([]peer.AddrInfo, *numPeers)

	// 1. Spawn Peers
	for i := 0; i < *numPeers; i++ {
		h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
		if err != nil {
			log.Fatalf("Failed to create host %d: %v", i, err)
		}
		defer h.Close()
		peers[i] = h
		addrs[i] = *host.InfoFromHost(h)
	}

	// 2. Connect Peers (Ring topology + Random links for robustness)
	log.Println("Connecting peers...")
	for i := 0; i < *numPeers; i++ {
		// Connect to next peer (Ring)
		next := (i + 1) % *numPeers
		if err := peers[i].Connect(ctx, addrs[next]); err != nil {
			log.Printf("Failed to connect %d -> %d: %v", i, next, err)
		}
		// Random additional link
		randIdx := rand.Intn(*numPeers)
		if randIdx != i && randIdx != next {
			peers[i].Connect(ctx, addrs[randIdx]) // Ignore error
		}
	}

	// 3. Setup GossipSub & Join Topic
	log.Println("Joining topics...")
	subs := make([]*pubsub.Subscription, *numPeers)
	topics := make([]*pubsub.Topic, *numPeers)

	for i, h := range peers {
		ps, err := pubsub.NewGossipSub(ctx, h)
		if err != nil {
			log.Fatalf("Failed to create pubsub for %d: %v", i, err)
		}
		t, err := ps.Join(*topicName)
		if err != nil {
			log.Fatalf("Failed to join topic %d: %v", i, err)
		}
		topics[i] = t
		sub, err := t.Subscribe()
		if err != nil {
			log.Fatalf("Failed to subscribe %d: %v", i, err)
		}
		subs[i] = sub

		// Start listener
		go func(idx int, s *pubsub.Subscription) {
			for {
				msg, err := s.Next(ctx)
				if err != nil {
					return
				}

				sentTime := int64(binary.BigEndian.Uint64(msg.Data[:8]))
				latency := time.Since(time.Unix(0, sentTime))

				collector.Add(latency)

				// Log sampling (don't spam)
				if rand.Float32() < 0.05 {
					// log.Printf("Peer %d received msg from %s. Latency: %v", idx, msg.ReceivedFrom, latency)
				}
			}
		}(i, sub)
	}

	// 4. Publisher Loop
	log.Println("Starting publication loop...")
	ticker := time.NewTicker(200 * time.Millisecond) // Higher ID rate for better stats
	defer ticker.Stop()

	finishTimer := time.After(*duration)

	pubIdx := 0
	for {
		select {
		case <-finishTimer:
			log.Println("Test finished. Generating report...")
			collector.Report()
			if err := collector.SaveCSV(); err != nil {
				log.Printf("Failed to save CSV: %v", err)
			}
			return
		case <-ticker.C:
			// Publish timestamp
			buf := make([]byte, 8)
			now := time.Now().UnixNano()
			binary.BigEndian.PutUint64(buf, uint64(now))

			if err := topics[pubIdx].Publish(ctx, buf); err != nil {
				log.Printf("Publish failed: %v", err)
			}
			pubIdx = (pubIdx + 1) % *numPeers // Rotate publisher
		}
	}
}
