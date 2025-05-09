package p2p

import (
	"context"
	"log"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"

	"peer_service/internal/media"
	"peer_service/internal/roles"
)

// Node bundles *all* runtime state that belongs to this peer.
// libp2p's host.Host is embedded so callers can still use the full Host API
// (ID(), Addrs(), EventBus(), Network(), …) without indirection.
//
//   - Everything libp2p already tracks stays in Host.
//   - Everything application‑specific lives in the struct below the mutex.
type Node struct {
	host.Host // embedded → brings ID(), Addrs() etc.

	// ------------------- mutable, app‑specific -------------------
	mu sync.RWMutex

	hlsPort     uint32 // 127.0.0.1:<port> for local mini‑HLS
	encoderLive bool   // ffmpeg running right now on *this* peer?

	// Continuity counter used when *this* peer becomes Streamer.
	nextSeq uint32

	// Runner management
	encoderRunner *media.EncoderRunner
	runnerCtx     context.Context    // Context for the current runner instance
	runnerCancel  context.CancelFunc // Used to stop the current runner

	// Gossipsub handles (lazy‑initialised)
	videoTopic *pubsub.Topic // /p2ptogether/video/1   (push 2‑s .ts)
	chatTopic  *pubsub.Topic // /p2ptogether/chat/<SID>
	ctrlTopic  *pubsub.Topic // /p2ptogether/control/<SID>

	// --- lightweight analytics counters (LAN‑MVP) ---
	bytesUp   uint64
	bytesDown uint64
}

// -------------- constructors --------------

func NewNode(h host.Host, hlsPort uint32) *Node {
	return &Node{
		Host:    h,
		hlsPort: hlsPort,
	}
}

// -------------- encoder / streaming helpers --------------

func (n *Node) EncoderLive() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.encoderLive
}

func (n *Node) SetEncoderLive(v bool) {
	n.mu.Lock()
	n.encoderLive = v
	n.mu.Unlock()
}

// Atomically fetch‑and‑increment the global media‑sequence counter.
// Used by QueueController when handing the stream over to a new Streamer.
func (n *Node) NextMediaSeq() uint32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	seq := n.nextSeq
	n.nextSeq++
	return seq
}

// EnsureRunnerState manages the ffmpeg encoder runner based on the desired state.
// If filePath is empty, it ensures the runner is stopped.
// Otherwise, it ensures the runner is active for filePath with seqBase if this peer should be streaming.
// It requires the RoleManager to check permissions.
func (n *Node) EnsureRunnerState(filePath string, seqBase uint32, qc *QueueController, rm *roles.RoleManager) {
	n.mu.Lock() // Node's main mutex
	defer n.mu.Unlock()

	if n.encoderRunner == nil {
		log.Println("EnsureRunnerState: Initializing EncoderRunner.")
		n.encoderRunner = media.NewEncoderRunner(n.hlsPort)
	}

	shouldRun := false
	if filePath != "" { // An empty filePath signals to stop.
		// Re-verify conditions based on current state.
		head, ok := qc.Q().Head() // Assuming Q() and Head() are safe or called under appropriate lock

		// Check permission using RoleManager
		nodePerms := rm.GetPermissionsForPeer(n.ID())

		// Check StoredBy and if this node has streaming permission
		if ok && head.FilePath == filePath && head.StoredBy == n.ID() && nodePerms.Has(roles.PermStream) {
			shouldRun = true
		}
	}

	// Get current runner status (needs its own lock if runner methods are not thread-safe w.r.t. Node's lock)
	// Assuming EncoderRunner methods are internally synchronized.
	isRunning := n.encoderRunner.IsRunning()
	currentFile := n.encoderRunner.GetCurrentFile()
	currentSeqBase := n.encoderRunner.GetCurrentSeqBase()

	if shouldRun {
		if isRunning && currentFile == filePath && currentSeqBase == seqBase {
			// log.Printf("EnsureRunnerState: Runner already active for %s, seqBase %d.", filePath, seqBase)
			return // Already running correctly
		}

		// Stop current runner if it exists or is for a different task
		if n.runnerCancel != nil {
			log.Printf("EnsureRunnerState: Restarting runner for new task: %s (seq %d). Old: %s (seq %d)", filePath, seqBase, currentFile, currentSeqBase)
			n.runnerCancel() // Signal existing runner to stop
			// Note: n.encoderRunner.Start is blocking. The old goroutine needs to exit.
			// A brief wait or a more robust mechanism might be needed if immediate restart is critical.
		}

		log.Printf("EnsureRunnerState: Starting new runner for %s, seqBase %d.", filePath, seqBase)
		n.runnerCtx, n.runnerCancel = context.WithCancel(context.Background())

		go func(ctx context.Context, path string, seq uint32, runner *media.EncoderRunner) {
			err := runner.Start(ctx, path, seq) // This is blocking
			if err != nil && err != context.Canceled {
				log.Printf("EnsureRunnerState: Encoder goroutine for %s exited with error: %v", path, err)
			}
		}(n.runnerCtx, filePath, seqBase, n.encoderRunner)
		n.encoderLive = true // Update Node's state (already under n.mu.Lock())
	} else { // Should not be running
		if isRunning { // If it's running but shouldn't be
			log.Printf("EnsureRunnerState: Runner active for %s but should stop. Stopping.", currentFile)
			if n.runnerCancel != nil {
				n.runnerCancel()
			}
		}
		n.encoderLive = false // Update Node's state
	}
}

// ReactToQueueUpdate checks the queue and node permissions (via RoleManager)
// and calls EnsureRunnerState accordingly.
func (n *Node) ReactToQueueUpdate(qc *QueueController, rm *roles.RoleManager) {
	log.Printf("Node.ReactToQueueUpdate: Checking runner state for peer %s", n.ID())
	head, ok := qc.Q().Head()

	// Get current node permissions
	nodePerms := rm.GetPermissionsForPeer(n.ID())

	log.Printf("ReactToQueueUpdate: Current Node ID: %s", n.ID())
	if ok { // if head item exists
		log.Printf("ReactToQueueUpdate: Queue head StoredBy: %s, FilePath: %s", head.StoredBy, head.FilePath)
	}
	log.Printf("ReactToQueueUpdate: Node has PermStream: %v", nodePerms.Has(roles.PermStream))
	if !ok {
		log.Println("Node.ReactToQueueUpdate: Queue is empty. Ensuring runner is stopped.")
		n.EnsureRunnerState("", 0, qc, rm)
		return
	}

	var seqBase uint32
	if head.StoredBy == n.ID() && nodePerms.Has(roles.PermStream) {
		seqBase = n.NextMediaSeq() // Or more sophisticated seqBase logic
		log.Printf("Node.ReactToQueueUpdate: Head item for me. Ensuring runner for: %s, seqBase %d", head.FilePath, seqBase)
		n.EnsureRunnerState(head.FilePath, seqBase, qc, rm)
	} else {
		log.Println("Node.ReactToQueueUpdate: Head item not for me or no PermStream. Ensuring runner is stopped.")
		n.EnsureRunnerState("", 0, qc, rm)
	}
}

// -------------- networking helpers --------------

// AttachPubSub stores the topics so that other packages (queue, chat, …)
// don’t have to pass them around explicitly.
func (n *Node) AttachPubSub(video, chat, ctrl *pubsub.Topic) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.videoTopic, n.chatTopic, n.ctrlTopic = video, chat, ctrl
}

func (n *Node) VideoTopic() *pubsub.Topic { n.mu.RLock(); defer n.mu.RUnlock(); return n.videoTopic }
func (n *Node) ChatTopic() *pubsub.Topic  { n.mu.RLock(); defer n.mu.RUnlock(); return n.chatTopic }
func (n *Node) CtrlTopic() *pubsub.Topic  { n.mu.RLock(); defer n.mu.RUnlock(); return n.ctrlTopic }

// -------------- analytics helpers --------------

func (n *Node) AddBytesUp(b uint64) {
	n.mu.Lock()
	n.bytesUp += b
	n.mu.Unlock()
}
func (n *Node) AddBytesDown(b uint64) {
	n.mu.Lock()
	n.bytesDown += b
	n.mu.Unlock()
}
func (n *Node) Traffic() (up, down uint64) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.bytesUp, n.bytesDown
}
