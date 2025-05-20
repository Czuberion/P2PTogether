package p2p

import (
	"context"
	"log"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"google.golang.org/protobuf/proto"

	"peer_service/internal/common"
	"peer_service/internal/media"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"
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

	hlsPort     uint32            // 127.0.0.1:<port> for local mini‑HLS
	ringBuffer  *media.RingBuffer // Reference to the global RingBuffer
	encoderLive bool              // ffmpeg running right now on *this* peer?

	// Continuity counter used when *this* peer becomes Streamer.
	// Should be managed by QueueController or passed in. For now, keep track locally.
	// nextMediaSeqForEncoder uint32

	// Runner management
	encoderRunner *media.EncoderRunner
	runnerCtx     context.Context    // Context for the current runner instance
	runnerCancel  context.CancelFunc // Used to stop the current runner

	// Gossipsub handles (lazy‑initialised)
	videoTopic *pubsub.Topic // /p2ptogether/video/1   (push 2‑s .ts)
	chatTopic  *pubsub.Topic // /p2ptogether/chat/<SID>
	ctrlTopic  *pubsub.Topic // /p2ptogether/control/<SID>

	// --- lightweight analytics counters ---
	bytesUp   uint64
	bytesDown uint64

	// --- Dependencies for reacting to queue changes ---
	// These are set from main.go to avoid circular dependencies
	// and allow Node to interact with higher-level components.
	queueControllerRef *QueueController
	roleManagerRef     *roles.RoleManager
	hubRef             *Hub
	// triggerDiscontinuityRef func() // This will be passed to QueueController.Handle
}

// -------------- constructors --------------

func NewNode(h host.Host, hlsPort uint32, rb *media.RingBuffer) *Node {
	return &Node{
		Host:       h,
		hlsPort:    hlsPort,
		ringBuffer: rb,
	}
}

// -------------- encoder / streaming helpers --------------

func (n *Node) EncoderLive() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.encoderLive
}

// EnsureRunnerState manages the ffmpeg encoder runner based on the desired state.
// If filePath is empty, it ensures the runner is stopped.
// Otherwise, it ensures the runner is active for filePath with seqBase if this peer should be streaming.
func (n *Node) EnsureRunnerState(filePath string, seqBase uint32) {
	n.mu.Lock() // Node's main mutex

	log.Printf("Node.EnsureRunnerState: Called with filePath='%s', seqBase=%d. n.encoderRunner is nil: %v", filePath, seqBase, n.encoderRunner == nil)

	if n.encoderRunner == nil {
		log.Println("Node.EnsureRunnerState: Initializing EncoderRunner.")
		n.encoderRunner = media.NewEncoderRunner(n.hlsPort)
		log.Printf("Node.EnsureRunnerState: n.encoderRunner after init: %p", n.encoderRunner)
	}

	// Critical: Capture these dependencies while Node.mu is locked.
	qc := n.queueControllerRef
	rm := n.roleManagerRef

	if qc == nil || rm == nil { // hub is used by the goroutine, not directly here
		log.Printf("Node.EnsureRunnerState: Missing dependencies (qc:%v, rm:%v). Cannot manage runner.", qc == nil, rm == nil)
		n.mu.Unlock() // Unlock before returning
		return
	}

	shouldRun := false
	if filePath != "" { // An empty filePath signals to stop.
		// Re-verify conditions based on current state.
		head, ok := qc.Q().Head()
		nodePerms := rm.GetPermissionsForPeer(n.ID())

		// Check StoredBy and if this node has streaming permission
		if ok && head.FilePath == filePath && head.StoredBy == n.ID() && nodePerms.Has(roles.PermStream) {
			shouldRun = true
		}
	}
	log.Printf("Node.EnsureRunnerState: Determined shouldRun = %v for filePath='%s'", shouldRun, filePath)

	// Store current runner context/cancel to manage existing runner
	existingRunnerCancel := n.runnerCancel

	// Read current runner's live attributes while n.mu is still locked
	isRunning := n.encoderRunner.IsRunning()
	currentFile := n.encoderRunner.GetCurrentFile()
	currentSeqBase := n.encoderRunner.GetCurrentSeqBase()
	log.Printf("Node.EnsureRunnerState: Current runner state: isRunning=%v, currentFile='%s', currentSeqBase=%d", isRunning, currentFile, currentSeqBase)

	if shouldRun {
		if isRunning && currentFile == filePath && currentSeqBase == seqBase {
			log.Printf("Node.EnsureRunnerState: Runner already active for %s (seq %d). No action.", filePath, seqBase)
			n.mu.Unlock()
			// Runner is already active for the correct file and sequence.
			return
		}

		log.Printf("Node.EnsureRunnerState: [%s] Proceeding to start/restart runner for %s", n.ID(), filePath)

		// --- New logic for starting a new file ---
		// This block executes if we are `shouldRun` and either not running, or running wrong file/seq.

		// 1. Calculate firstSeq for this new stream instance.
		//    This should come from the global media sequence managed by QueueController.
		currentGlobalMediaSequence := qc.GetNextStartSequence()
		calculatedFirstSeq := currentGlobalMediaSequence
		// In a more advanced model, if seqBase was part of headItem and non-zero, use that.
		// For now, always use the global sequence.

		// 2. Calculate originSec for this item.
		//    start_pts for a new item is typically 0.
		calculatedOriginSec := float64(calculatedFirstSeq) * media.SegmentDuration.Seconds() // Assuming start_pts is 0

		// 3. Update the QueueItem in the QueueController's queue with first_seq. NumSegs should already be there.
		//    This is critical so that other parts of the system (like seek logic) know this item's mapping.
		//    The `head` variable here is from qc.Q().Head() before the Node.mu lock.
		//    We need to get the current head again or ensure `filePath` matches current head.
		log.Printf("Node.EnsureRunnerState: [%s] Attempting to update QueueItem FirstSeq...", n.ID())
		if currentHeadItem, ok := qc.Q().Head(); ok {
			currentHeadItem.FirstSeq = calculatedFirstSeq
			// currentHeadItem.NumSegs is assumed to be set on APPEND.
			qc.Q().SetItemAtCursor(currentHeadItem) // Updates the item at the current cursor

			// After updating the item, broadcast new QueueUpdate
			if localHub := n.hubRef; localHub != nil { // n.hubRef should be qc.hubRef if passed consistently
				log.Printf("Node.EnsureRunnerState: [%s] Broadcasting QueueUpdate after setting FirstSeq for %s to %d", n.ID(), filePath, calculatedFirstSeq)
				localHub.Broadcast(qc.Snapshot()) // qc.Snapshot() will reflect the updated item
				log.Printf("Node.EnsureRunnerState: [%s] Broadcasted QueueUpdate to localHub.", n.ID())
			}

			// Use a general background context for these initial broadcasts
			initialBroadcastCtx := context.Background()

			if ctrlTopic := n.ctrlTopic; ctrlTopic != nil {
				if marshalledMsg, err := proto.Marshal(qc.Snapshot()); err == nil {
					if errPub := ctrlTopic.Publish(initialBroadcastCtx, marshalledMsg); errPub != nil {
						log.Printf("Node.EnsureRunnerState: [%s] ERROR publishing QueueUpdate to ctrlTopic: %v", n.ID(), errPub)
					} else {
						log.Printf("Node.EnsureRunnerState: [%s] Published QueueUpdate to ctrlTopic.", n.ID())
					}
				} else {
					log.Printf("Node.EnsureRunnerState: [%s] ERROR marshalling QueueUpdate for ctrlTopic: %v", n.ID(), err)
				}
			} else {
				log.Printf("Node.EnsureRunnerState: [%s] ctrlTopic is NIL, cannot publish QueueUpdate.", n.ID())
			}
			log.Printf("Node.EnsureRunnerState: [%s] QueueItem FirstSeq updated for %s to %d.", n.ID(), filePath, calculatedFirstSeq)
		} else {
			log.Printf("Node.EnsureRunnerState: [%s] Could not update QueueItem for %s; head mismatch or queue empty.", n.ID(), filePath)
		}
		log.Printf("Node.EnsureRunnerState: [%s] Finished QueueItem update attempt.", n.ID())

		// Stop current runner if it exists or is for a different task
		if existingRunnerCancel != nil {
			log.Printf("Node.EnsureRunnerState: Stopping existing runner for %s (seq %d) to start/restart for %s (seq %d).", currentFile, currentSeqBase, filePath, seqBase)
			existingRunnerCancel() // Signal existing runner to stop
			// We don't wait for it to finish here; the new context will take over.
			// The old goroutine should detect cancellation and exit.
		}

		// // Trigger discontinuity for the new stream on this node's HLS server.
		// // This is important if this node itself is also a viewer of its own stream.
		// if n.queueControllerRef != nil && n.queueControllerRef.triggerDiscontinuity != nil {
		// 	log.Printf("Node.EnsureRunnerState: Triggering local HLS discontinuity for new stream %s (seq %d)", filePath, seqBase)
		// 	n.queueControllerRef.triggerDiscontinuity()
		// }
		log.Printf("Node.EnsureRunnerState: [%s] Existing runner (if any) cancellation requested.", n.ID())

		// 4. Reset this node's RingBuffer with the new firstSeq.
		if n.ringBuffer != nil {
			log.Printf("Node.EnsureRunnerState: [%s] Resetting RingBuffer to new base sequence: %d for file %s", n.ID(), calculatedFirstSeq, filePath)
			n.ringBuffer.Reset(calculatedFirstSeq)
		}
		// Trigger HLS discontinuity if this node is starting a new stream.
		if qc.triggerDiscontinuity != nil { // qc is n.queueControllerRef
			log.Printf("Node.EnsureRunnerState: [%s] Triggering HLS discontinuity for new stream %s (new firstSeq %d)", n.ID(), filePath, calculatedFirstSeq)
			qc.triggerDiscontinuity()
		}
		log.Printf("Node.EnsureRunnerState: [%s] RingBuffer reset and discontinuity triggered.", n.ID())

		// 5. Emit PlaylistReset
		playlistResetMsg := &p2ppb.PlaylistReset{
			Sequence:  calculatedFirstSeq,
			StartPts:  0.0, // New files start at 0.0 relative to their own content
			OriginSec: calculatedOriginSec,
			HlcTs:     common.GetCurrentHLC(),
		}
		// Use context.Background() for broadcasts not tied to a specific runner's lifecycle.
		// broadcastCtx is already defined above for the QueueUpdate broadcast. We can reuse it.
		broadcastCtx := context.Background() // Not needed if already defined
		serverPayload := &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_PlaylistReset{PlaylistReset: playlistResetMsg}}
		log.Printf("Node.EnsureRunnerState: [%s] Broadcasting PlaylistReset for %s. Sequence: %d, OriginSec: %.2f", n.ID(), filePath, calculatedFirstSeq, calculatedOriginSec)
		if hub := n.hubRef; hub != nil {
			hub.Broadcast(serverPayload)
		} // n.hubRef is qc.hubRef
		if ctrlTopic := n.ctrlTopic; ctrlTopic != nil {
			if marshalledMsg, err := proto.Marshal(serverPayload); err == nil {
				if errPub := ctrlTopic.Publish(broadcastCtx, marshalledMsg); errPub != nil {
					log.Printf("Node.EnsureRunnerState: [%s] ERROR publishing PlaylistReset to ctrlTopic: %v", n.ID(), errPub)
				} else {
					log.Printf("Node.EnsureRunnerState: [%s] Published PlaylistReset to ctrlTopic.", n.ID())
				}
			} else {
				log.Printf("Node.EnsureRunnerState: [%s] ERROR marshalling PlaylistReset for ctrlTopic: %v", n.ID(), err)
			}
		} else {
			log.Printf("Node.EnsureRunnerState: [%s] ctrlTopic is NIL, cannot publish PlaylistReset.", n.ID())
		}
		log.Printf("Node.EnsureRunnerState: [%s] PlaylistReset broadcast initiated.", n.ID())

		targetSeqForRunner := calculatedFirstSeq
		log.Printf("Node.EnsureRunnerState: [%s] Preparing to start new runner for %s, actual seqBase for ffmpeg: %d.", n.ID(), filePath, targetSeqForRunner)
		newCtx, newCancel := context.WithCancel(context.Background())
		n.runnerCtx = newCtx
		n.runnerCancel = newCancel
		n.encoderLive = true

		// Local copies for the goroutine
		localRunner := n.encoderRunner
		localNodeID := n.ID()
		localQC := qc
		localRM := rm
		localHub := n.hubRef

		n.mu.Unlock() // Unlock Node.mu before starting the goroutine

		go func(ctx context.Context, path string, seq uint32, runner *media.EncoderRunner) {
			log.Printf("Node.EnsureRunnerState: Goroutine: Starting EncoderRunner.Start for %s (seq %d)", path, seq)
			runErr := runner.Start(ctx, path, seq) // This is blocking

			// After runner.Start() returns (EOF, error, or cancellation)
			n.mu.Lock() // Lock Node to safely access its state
			// Check if the context of *this specific goroutine* was the one that got cancelled,
			// or if it was superseded by another runner instance for this node.
			// If n.runnerCtx is no longer ctx, it means a new runner has been started for this node.
			if n.runnerCtx != ctx {
				log.Printf("Node.EnsureRunnerState: Goroutine for %s (seq %d) was superseded. Exiting.", path, seq)
				n.mu.Unlock()
				// TODO: Consider if the superseding runner needs a kick via ReactToQueueUpdate if this was an unexpected exit.
				return
			}
			// If it is still the active context for this node's runner:
			n.encoderLive = false // Set under lock
			n.mu.Unlock()         // Unlock Node

			if runErr == nil && ctx.Err() == nil { // Successful EOF & context not cancelled during run
				log.Printf("Node.EnsureRunnerState: Goroutine: Encoder for %s (seq %d) finished with EOF. Triggering QueueCmd_NEXT.", path, seq)

				// Report the next sequence number from this stream to the QueueController
				// so it can update the global media sequence for the *next* streamer.
				if runner != nil && localQC != nil { // runner is n.encoderRunner
					// localQC.UpdateGlobalMediaSequence(n.ringBuffer.GetNextSeq()) // Use node's ringbuffer's nextSeq
					// Read ringBuffer under Node's lock
					if n.ringBuffer != nil {
						finishedStreamNextSeq := n.ringBuffer.GetNextSeq()
						localQC.UpdateGlobalMediaSequence(finishedStreamNextSeq)
					}
				}

				// Use the captured localQC, localRM, localHub
				// For ctrlTopic, read it dynamically using the getter to ensure it's current,
				// as AttachPubSub could be called.
				var currentCtrlTopic *pubsub.Topic
				n.mu.RLock() // Lock to safely read n.ctrlTopic
				currentCtrlTopic = n.ctrlTopic
				n.mu.RUnlock()

				if localQC == nil || localRM == nil || localHub == nil || currentCtrlTopic == nil {
					log.Printf("Node.EnsureRunnerState: Goroutine: Missing dependencies to trigger NEXT for %s. qcIsNil:%v, rmIsNil:%v, hubIsNil:%v, topicIsNil:%v", path, localQC == nil, localRM == nil, localHub == nil, currentCtrlTopic == nil)
					return
				}

				nextCmd := &p2ppb.QueueCmd{
					Type:  p2ppb.QueueCmd_NEXT,
					HlcTs: common.GetCurrentHLC(),
				}
				// Use a new context for Handle, as the runner's ctx might be done.
				// Or pass context.Background() if Handle doesn't need specific cancellation from here.
				// err := currentQC.Handle(context.Background(), nextCmd, localNodeID, currentRM, currentHub, n, currentCtrlTopic)
				err := localQC.Handle(context.Background(), nextCmd, localNodeID, localRM, localHub, n, currentCtrlTopic)
				if err != nil {
					log.Printf("Node.EnsureRunnerState: Goroutine: Error triggering QueueCmd_NEXT for %s: %v", path, err)
				}
			} else if runErr == context.Canceled {
				log.Printf("Node.EnsureRunnerState: Goroutine: Encoder for %s (seq %d) was cancelled.", path, seq)
			} else {
				log.Printf("Node.EnsureRunnerState: Goroutine: Encoder for %s (seq %d) exited with error: %v", path, seq, runErr)
				// Consider if any queue action is needed on other errors (e.g., skip item)
			}
			// }(n.runnerCtx, filePath, seqBase, localRunner)
		}(n.runnerCtx, filePath, targetSeqForRunner, localRunner) // Pass targetSeqForRunner
		// }

	} else { // Should not be running
		if isRunning {
			log.Printf("Node.EnsureRunnerState: Runner is active for %s (seq %d) but should stop. Stopping.", currentFile, currentSeqBase)

			if existingRunnerCancel != nil {
				existingRunnerCancel()
			}
			// The existing goroutine will handle setting n.encoderLive to false upon cancellation.
			// However, if we are here because filePath is empty (explicit stop signal),
			// we should ensure encoderLive is false.
			n.encoderLive = false
		} else {
			n.encoderLive = false // Ensure it's false if not supposed to run and not already running
		}
		n.mu.Unlock() // Unlock if shouldRun was false
	}
}

// StopCurrentEncoder explicitly stops the currently running encoder, if any.
// This is called by QueueController for SKIP_NEXT or other admin actions.
func (n *Node) StopCurrentEncoder() {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.runnerCancel != nil {
		log.Printf("Node.StopCurrentEncoder: Sending cancel signal to current encoder runner.")
		n.runnerCancel()
		// The runner's goroutine is responsible for its lifecycle post-cancellation.
	} else {
		log.Printf("Node.StopCurrentEncoder: No active encoder runner to stop.")
		// Ensure encoderLive is false if there's no cancel function (implies no runner)
		n.encoderLive = false
	}
}

// ReactToQueueUpdate checks the queue and node permissions and calls EnsureRunnerState.
func (n *Node) ReactToQueueUpdate() {
	log.Printf("Node.ReactToQueueUpdate: Checking runner state for peer %s", n.ID())

	log.Printf("Node.ReactToQueueUpdate: [%s] Attempting to RLock n.mu", n.ID())
	n.mu.RLock()
	log.Printf("Node.ReactToQueueUpdate: [%s] Successfully RLock'ed n.mu", n.ID())
	qc := n.queueControllerRef
	rm := n.roleManagerRef
	n.mu.RUnlock()
	log.Printf("Node.ReactToQueueUpdate: [%s] Released RLock n.mu", n.ID())

	if n.ringBuffer == nil {
		log.Printf("Node.ReactToQueueUpdate: [%s] RingBuffer reference is nil. Cannot determine next sequence. Aborting.", n.ID())
		return
	}

	if qc == nil || rm == nil {
		log.Printf("Node.ReactToQueueUpdate: [%s] Missing QueueController (isNil: %v) or RoleManager (isNil: %v) reference. Cannot react.", n.ID(), qc == nil, rm == nil)
		return
	}

	log.Printf("Node.ReactToQueueUpdate: [%s] Attempting to get queue head", n.ID())
	head, ok := qc.Q().Head()
	log.Printf("Node.ReactToQueueUpdate: [%s] Got queue head (ok: %v)", n.ID(), ok)
	if !ok {
		log.Printf("Node.ReactToQueueUpdate: [%s] Queue is empty. Ensuring runner is stopped.", n.ID())
		n.EnsureRunnerState("", 0) // Empty filePath signals stop
		return
	}

	nodePerms := rm.GetPermissionsForPeer(n.ID())
	log.Printf("Node.ReactToQueueUpdate: [%s] Current Node ID: %s. Queue head StoredBy: %s, FilePath: %s. Has PermStream: %v", n.ID(), n.ID(), head.StoredBy, head.FilePath, nodePerms.Has(roles.PermStream))

	if head.StoredBy == n.ID() && nodePerms.Has(roles.PermStream) {
		// This node should stream the current head item.

		// Get the next global media sequence number from the RingBuffer.
		// seqBaseToUse := n.ringBuffer.GetNextSeq()
		// log.Printf("Node.ReactToQueueUpdate: [%s] Using seqBase %d from RingBuffer.GetNextSeq()", n.ID(), seqBaseToUse)

		// Get the next global media sequence number from the QueueController.
		seqBaseToUse := qc.GetNextStartSequence()
		log.Printf("Node.ReactToQueueUpdate: [%s] Using global seqBase %d from QueueController for new stream", n.ID(), seqBaseToUse)

		log.Printf("Node.ReactToQueueUpdate: [%s] Head item for me. Ensuring runner for: %s, seqBase %d", n.ID(), head.FilePath, seqBaseToUse)
		n.EnsureRunnerState(head.FilePath, seqBaseToUse)
	} else {
		log.Printf("Node.ReactToQueueUpdate: [%s] Head item not for me or no PermStream. Ensuring runner is stopped.", n.ID())
		n.EnsureRunnerState("", 0) // Empty filePath signals stop
	}
}

// SetDependencies allows injecting references to other components.
func (n *Node) SetDependencies(qc *QueueController, rm *roles.RoleManager, hub *Hub) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.queueControllerRef = qc
	n.roleManagerRef = rm
	n.hubRef = hub
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

func (n *Node) RingBuffer() *media.RingBuffer {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.ringBuffer
}
