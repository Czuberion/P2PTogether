// peer_service/internal/p2p/signaling_test.go
package p2p

import (
	"context"
	"testing"

	"peer_service/internal/roles"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// TestBatonPass_Signaling verifies that the QueueController advances the queue
// only when both Encoder and Player completion conditions are met.
func TestBatonPass_Signaling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Setup minimal Host and PubSub
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		t.Fatalf("Failed to create pubsub: %v", err)
	}

	// 2. Init Components
	rm := roles.NewRoleManager()
	hub := NewHub()
	// Mock triggerDiscontinuity
	qc := NewQueueController(func() {})

	node := NewNode(h, ps, 8080, nil)
	node.SetDependencies(qc, rm, hub)

	// Join a dummy session to initialize topics
	err = node.JoinSessionTopics("test-session")
	if err != nil {
		t.Fatalf("Failed to join session: %v", err)
	}

	qc.SetDependencies(node, hub, rm, node.CtrlTopic())

	// 3. Populate Queue with 2 items
	rm.UpdateDefinition("Admin", roles.PermQueue|roles.PermStream)
	rm.SetPeerRoles(node.ID(), []string{"Admin"})

	item1 := QueueItem{
		FilePath: "video1.mp4",
		StoredBy: node.ID(),
		AddedBy:  node.ID(),
		FirstSeq: 0,
		NumSegs:  10,
	}
	item2 := QueueItem{
		FilePath: "video2.mp4",
		StoredBy: node.ID(),
		AddedBy:  node.ID(),
		FirstSeq: 10,
		NumSegs:  10,
	}

	// Directly manipulate queue for setup
	qc.q.Append(item1)
	qc.q.Append(item2)

	// Verify Init State
	head, _ := qc.q.Head()
	if head.FilePath != "video1.mp4" {
		t.Fatalf("Expected head to be video1.mp4")
	}

	// 4. Simulate Encoder Finish for Item 1 (Seq 0)
	// This simulates the local encoder finishing.
	qc.NotifyEncoderFinished(0, node.ID())

	// Assert: Queue should NOT have advanced yet (waiting for player)
	head, _ = qc.q.Head()
	if head.FilePath != "video1.mp4" {
		t.Errorf("Queue advanced prematurely after only Encoder finished")
	}

	// 5. Simulate Player Finish for Item 1 (Seq 0)
	// This simulates the local player finishing.
	qc.NotifyPlayerStreamCompleted(0, node.ID())

	// Assert: Queue SHOULD advance to Item 2
	// Allow tiny delay for async handling (Handle uses goroutines? No, Handle is sync usually,
	// but Notify... calls checkAndAttemptQueueAdvance which calls Handle).
	// But checkAndAttemptQueueAdvance locks mu, so it's sync.

	head, ok := qc.q.Head()
	if !ok {
		t.Fatalf("Queue became empty, expected item2")
	}
	if head.FilePath != "video2.mp4" {
		t.Errorf("Queue did not advance to video2.mp4 after Baton Pass. Current head: %s", head.FilePath)
	}
}
