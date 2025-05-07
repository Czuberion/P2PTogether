package p2p

import (
	"context"
	"log"
	"reflect"
	"sync"
	"time"

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

	roles        []*roles.Role // active role set (copy, detached from map)
	rolesUpdated time.Time     // monotonic; for conflict‑resolution

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
	// default to a single Viewer role
	roles.AllRolesMu.RLock()
	viewer := roles.AllRoles["viewer"]
	roles.AllRolesMu.RUnlock()

	return &Node{
		Host:    h,
		hlsPort: hlsPort,
		roles:   []*roles.Role{viewer},
	}
}

// -------------- role / permission helpers --------------

// Roles returns a copy of the current role set.
func (n *Node) Roles() []*roles.Role {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]*roles.Role, len(n.roles))
	copy(out, n.roles)
	return out
}

// SetRoles replaces the role set and returns true if it really changed.
func (n *Node) SetRoles(rs []*roles.Role) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if reflect.DeepEqual(rs, n.roles) {
		return false
	}
	// store a detached copy
	n.roles = append([]*roles.Role(nil), rs...)
	n.rolesUpdated = time.Now()
	return true
}

// Permissions returns the OR‑ed permission mask that results from the
// active role set.  Callers must *not* cache the result.
func (n *Node) Permissions() roles.Permission {
	n.mu.RLock()
	perms := roles.PermissionsForRoles(n.roles...)
	n.mu.RUnlock()
	return perms
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
func (n *Node) EnsureRunnerState(filePath string, seqBase uint32, qc *QueueController) {
	n.mu.Lock() // Node's main mutex
	defer n.mu.Unlock()

	if n.encoderRunner == nil {
		log.Println("EnsureRunnerState: Initializing EncoderRunner.")
		n.encoderRunner = media.NewEncoderRunner(n.hlsPort)
	}

	shouldRun := false
	if filePath != "" { // An empty filePath signals to stop.
		// This logic is specific to "if I am StoredBy for head and have PermStream"
		// It's called by handleLocalQueueUpdateForRunner which already checks this.
		// For more generic use, filePath and seqBase would be the sole drivers.
		// Here, we re-verify based on current queue state.
		head, ok := qc.Q().Head() // Assuming Q() and Head() are safe or called under appropriate lock
		if ok && head.FilePath == filePath && head.StoredBy == n.ID() && roles.HasPermissionFromRoles(roles.PermStream, n.roles...) {
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
