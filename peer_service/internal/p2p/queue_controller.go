package p2p

import (
	"context"
	"fmt"
	"log"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"peer_service/internal/common"
	"peer_service/internal/roles"
	clientpb "peer_service/proto"
	p2ppb "peer_service/proto/p2p"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

// PlaybackCompletionState tracks EOF reports for a given stream.
type PlaybackCompletionState struct {
	EncoderFinished       bool
	PlayerCompletedByPeer map[peer.ID]bool // Tracks which peers reported player EOF
	HlcTsEncoderFinished  int64
}

// PlaybackStateInfo is a simplified struct to hold essential playback state.
type PlaybackStateInfo struct {
	StreamSequenceID uint32
	IsPlaying        bool
	HlcTs            int64
}

// QueueController manages the media queue, its state transitions, and interactions with the Node.
type QueueController struct {
	q                    *Queue
	triggerDiscontinuity func() // Function to call for HLS discontinuity

	// Global media sequence management
	// mu                   sync.Mutex // Protects currentMediaSequence
	// currentMediaSequence uint32     // The next sequence number to be assigned to a new stream segment from a new ffmpeg instance.

	globalSeqMu          sync.Mutex // Protects currentMediaSequence
	currentMediaSequence uint32     // The next sequence number for a new stream.

	// Playback state and completion tracking
	lastPlaybackStateMu    sync.RWMutex // Protects lastKnownPlaybackState
	lastKnownPlaybackState PlaybackStateInfo

	completionMu           sync.Mutex
	streamCompletionStates map[uint32]*PlaybackCompletionState // Key: stream_sequence_id

	// Dependencies (set via method or constructor)
	nodeRef      *Node // Reference to the local Node for actions like ReactToQueueUpdate
	hubRef       *Hub
	rmRef        *roles.RoleManager
	ctrlTopicRef *pubsub.Topic
}

// NewQueueController creates a new QueueController.
func NewQueueController(triggerDisc func()) *QueueController {
	return &QueueController{
		q:                      NewQueue(),
		triggerDiscontinuity:   triggerDisc,
		streamCompletionStates: make(map[uint32]*PlaybackCompletionState),
	}
}

// Q returns the underlying queue. Used for read-only operations like checking head.
func (qc *QueueController) Q() *Queue {
	return qc.q
}

// SetDependencies injects necessary components.
func (qc *QueueController) SetDependencies(node *Node, hub *Hub, rm *roles.RoleManager, topic *pubsub.Topic) {
	qc.nodeRef = node
	qc.hubRef = hub
	qc.rmRef = rm
	qc.ctrlTopicRef = topic
}

// GetNextStartSequence provides the sequence number that a new ffmpeg instance
// (for a new queue item) should use for its -start_number.
func (qc *QueueController) GetNextStartSequence() uint32 {
	qc.globalSeqMu.Lock()
	defer qc.globalSeqMu.Unlock()
	// For now, simply return the current. The responsibility to update it after a stream
	// is complex and depends on knowing how many segments the *previous* stream had.
	// This will be refined. For starting a *new* distinct stream, this sequence is used.
	return qc.currentMediaSequence
}

// UpdateGlobalMediaSequence sets the global media sequence. This should be called
// by the Node that just finished streaming its item, reporting its RingBuffer's nextSeq.
func (qc *QueueController) UpdateGlobalMediaSequence(lastStreamNextSeq uint32) {
	qc.globalSeqMu.Lock()
	defer qc.globalSeqMu.Unlock()
	if lastStreamNextSeq > qc.currentMediaSequence {
		log.Printf("QueueController: Updating global media sequence from %d to %d", qc.currentMediaSequence, lastStreamNextSeq)
		qc.currentMediaSequence = lastStreamNextSeq
	}
}

// UpdateLastKnownPlaybackState should be called when the authoritative PlaybackStateCmd is processed
func (qc *QueueController) UpdateLastKnownPlaybackState(newState *p2ppb.SetPlaybackStateCmd) {
	if newState == nil {
		return
	}
	qc.lastPlaybackStateMu.Lock()
	defer qc.lastPlaybackStateMu.Unlock()
	// Only update if the new HLC is newer, or if it's for a different stream sequence ID
	if newState.HlcTs > qc.lastKnownPlaybackState.HlcTs || newState.StreamSequenceId != qc.lastKnownPlaybackState.StreamSequenceID {
		qc.lastKnownPlaybackState = PlaybackStateInfo{ // Assuming PlaybackStateInfo is defined elsewhere (e.g. node.go or a common place)
			StreamSequenceID: newState.StreamSequenceId,
			IsPlaying:        newState.TargetIsPlaying,
			HlcTs:            newState.HlcTs,
		}
		log.Printf("QueueController: Updated lastKnownPlaybackState for stream %d, IsPlaying: %v, HLC: %d", newState.StreamSequenceId, newState.TargetIsPlaying, newState.HlcTs)
	}
}

func (qc *QueueController) GetLastKnownPlaybackState() PlaybackStateInfo {
	qc.lastPlaybackStateMu.RLock()
	defer qc.lastPlaybackStateMu.RUnlock()
	return qc.lastKnownPlaybackState
}

// NotifyEncoderFinished is called by the Node when its local ffmpeg encoder finishes a stream.
func (qc *QueueController) NotifyEncoderFinished(streamSeqID uint32, encoderPeerID peer.ID) {
	log.Printf("QueueController: Encoder on peer %s reported finished for stream %d", encoderPeerID, streamSeqID)
	qc.completionMu.Lock()
	if _, ok := qc.streamCompletionStates[streamSeqID]; !ok {
		qc.streamCompletionStates[streamSeqID] = &PlaybackCompletionState{PlayerCompletedByPeer: make(map[peer.ID]bool)}
	}
	qc.streamCompletionStates[streamSeqID].EncoderFinished = true
	qc.streamCompletionStates[streamSeqID].HlcTsEncoderFinished = common.GetCurrentHLC()
	qc.completionMu.Unlock()

	qc.checkAndAttemptQueueAdvance(streamSeqID)
}

// NotifyPlayerStreamCompleted is called when a client reports its player finished a stream.
func (qc *QueueController) NotifyPlayerStreamCompleted(streamSeqID uint32, reportingPeerID peer.ID) {
	log.Printf("QueueController: Player on peer %s reported completion for stream %d", reportingPeerID, streamSeqID)
	qc.completionMu.Lock()
	if _, ok := qc.streamCompletionStates[streamSeqID]; !ok {
		qc.streamCompletionStates[streamSeqID] = &PlaybackCompletionState{PlayerCompletedByPeer: make(map[peer.ID]bool)}
	}
	qc.streamCompletionStates[streamSeqID].PlayerCompletedByPeer[reportingPeerID] = true
	qc.completionMu.Unlock()

	qc.checkAndAttemptQueueAdvance(streamSeqID)
}

func (qc *QueueController) checkAndAttemptQueueAdvance(streamSeqID uint32) {
	qc.completionMu.Lock()
	state, exists := qc.streamCompletionStates[streamSeqID]
	if !exists {
		qc.completionMu.Unlock()
		return // No state for this stream, shouldn't happen if called correctly
	}

	// Check if this stream is still the head of the queue
	currentHead, headOk := qc.q.Head() // q has its own RLock for Head()
	if !headOk || currentHead.FirstSeq != streamSeqID {
		log.Printf("QueueController: checkAndAttemptQueueAdvance for stream %d, but it's no longer head (current head: %d). No action.", streamSeqID, currentHead.FirstSeq)
		// Clean up old state if necessary, or let it be overwritten by new stream states
		delete(qc.streamCompletionStates, streamSeqID)
		qc.completionMu.Unlock()
		return
	}
	streamerPeerID := currentHead.StoredBy
	encoderDone := state.EncoderFinished
	streamerPlayerDone := state.PlayerCompletedByPeer[streamerPeerID]
	qc.completionMu.Unlock() // Unlock before potentially long-running Handle or React

	if encoderDone && streamerPlayerDone {
		log.Printf("QueueController: Conditions met for stream %d (EncoderDone: %v, StreamerPlayerDone: %v). Advancing queue.", streamSeqID, encoderDone, streamerPlayerDone)
		// Use context.Background() for internal command handling
		// Ensure qc.nodeRef, qc.rmRef, qc.hubRef, qc.ctrlTopicRef are non-nil
		if qc.nodeRef == nil || qc.rmRef == nil || qc.hubRef == nil || qc.ctrlTopicRef == nil {
			log.Printf("QueueController: checkAndAttemptQueueAdvance: Missing critical dependencies (nodeRef, rmRef, hubRef, or ctrlTopicRef). Cannot advance queue for stream %d.", streamSeqID)
			return
		}
		cmd := &p2ppb.QueueCmd{Type: p2ppb.QueueCmd_NEXT, HlcTs: common.GetCurrentHLC()}

		if err := qc.Handle(context.Background(), cmd, qc.nodeRef.ID(), qc.rmRef, qc.hubRef, qc.nodeRef, qc.ctrlTopicRef); err != nil {
			log.Printf("QueueController: Error during self-initiated NEXT for stream %d: %v", streamSeqID, err)
		}
		// Clean up completion state for the processed stream
		qc.completionMu.Lock()
		delete(qc.streamCompletionStates, streamSeqID)
		qc.completionMu.Unlock()
	} else {
		log.Printf("QueueController: Conditions NOT YET met for stream %d (EncoderDone: %v, StreamerPlayerDone: %v for peer %s). Waiting.", streamSeqID, encoderDone, streamerPlayerDone, streamerPeerID)
	}
}

func getDurationSeconds(filePath string) (float64, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", filePath)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("ffprobe error for %s: %v, output: %s", filePath, err, string(output))
		return 0, err
	}
	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		log.Printf("ffprobe parse duration error for %s (%s): %v", filePath, durationStr, err)
		return 0, err
	}
	log.Printf("ffprobe: %s duration: %.2f seconds", filePath, duration)
	return duration, nil
}

const defaultSegmentDurationSeconds = 2.0 // Make this configurable or share from media package if possible

func (qc *QueueController) Snapshot() *clientpb.ServerMsg {
	it := qc.q.Items()
	pbItems := make([]*p2ppb.QueueItem, 0, len(it))
	for _, x := range it {
		pbItems = append(pbItems, &p2ppb.QueueItem{
			FilePath: x.FilePath,
			StoredBy: x.StoredBy.String(),
			AddedBy:  x.AddedBy.String(),
			FirstSeq: x.FirstSeq,
			NumSegs:  x.NumSegs,
			HlcTs:    x.HlcTs,
		})
	}
	// HlcTs for QueueUpdate itself will be set by caller
	return &clientpb.ServerMsg{Payload: &clientpb.ServerMsg_QueueUpdate{
		QueueUpdate: &p2ppb.QueueUpdate{Items: pbItems, HlcTs: common.GetCurrentHLC()}}}
}

// Handle processes a queue command, mutates the queue, and notifies local/remote clients.
// It is called when a local gRPC client sends a QueueCmd.
// It uses the RoleManager to check permissions.
func (qc *QueueController) Handle(ctx context.Context, cmd *p2ppb.QueueCmd, sender peer.ID, rm *roles.RoleManager, hub *Hub, node *Node, ctrlTopic *pubsub.Topic) error {
	// Permission check for user-initiated commands
	isInternalNext := cmd.Type == p2ppb.QueueCmd_NEXT && sender == node.ID() // Check if it's an internal NEXT
	if !isInternalNext {
		perms := rm.GetPermissionsForPeer(sender)
		if !perms.Has(roles.PermQueue) {
			return fmt.Errorf("permission denied: peer %s missing Queue perm for cmd type %s", sender, cmd.Type)
		}
	}

	madeChange := false
	currentHeadBeforeChange, headExistedBeforeChange := qc.q.Head()
	cursorBeforeChange := qc.q.GetCursor()

	switch cmd.Type {
	case p2ppb.QueueCmd_APPEND:
		if cmd.FilePath == "" {
			break
		}

		durationSec, err := getDurationSeconds(cmd.FilePath)
		numSegs := uint32(0)
		if err == nil && durationSec > 0 {
			numSegs = uint32(math.Ceil(durationSec / defaultSegmentDurationSeconds))
		} else {
			log.Printf("QueueController: Could not get duration for %s or duration is zero, num_segs will be 0. Error: %v", cmd.FilePath, err)
		}

		qc.q.Append(QueueItem{
			FilePath: cmd.FilePath,
			StoredBy: sender,
			AddedBy:  sender,
			NumSegs:  numSegs,
			HlcTs:    cmd.HlcTs,
		})
		madeChange = true
	case p2ppb.QueueCmd_REMOVE:
		if qc.q.Remove(int(cmd.Index)) {
			madeChange = true
		}
	case p2ppb.QueueCmd_CLEAR:
		qc.q.Clear()
		madeChange = true
		// After clearing, the current streamer (if any) should stop.
		node.StopCurrentEncoder() // Explicitly stop encoder.
	case p2ppb.QueueCmd_NEXT: // Typically server-initiated after EOF
		log.Printf("QueueController: Processing NEXT command from %s. Current cursor: %d", sender, qc.q.GetCursor())

		// UpdateGlobalMediaSequence is now called when the encoder finishes.
		// If NEXT is from an external source (admin), we might not have direct ring buffer access.
		if node != nil && node.Host.ID() == sender && node.RingBuffer() != nil {
			// qc.UpdateGlobalMediaSequence(node.RingBuffer().GetNextSeq()) // This might be redundant if called at encoder EOF
		}

		newHeadItem, advanced := qc.q.AdvanceCursor()
		if advanced {
			madeChange = true
			log.Printf("QueueController: Advanced cursor for NEXT. New cursor: %d, Item: %s", qc.q.GetCursor(), newHeadItem.FilePath)
		} else {
			log.Println("QueueController: NEXT called, but cursor did not advance (reached end or empty). No change.")
			// If queue becomes empty or cursor is invalid, ensure encoder stops
			node.StopCurrentEncoder()
		}

	case p2ppb.QueueCmd_SKIP_PREV: // User-initiated skip backward
		log.Printf("QueueController: Processing SKIP_PREV command from %s. Current cursor: %d", sender, qc.q.GetCursor())

		itemBeingSkipped, wasPlaying := qc.q.Head()
		node.StopCurrentEncoder() // Stop current playback first

		if wasPlaying {
			// If this node instance was the one streaming the item that's now being skipped.
			// Update global sequence based on its ring buffer's state *before* rewinding.
			if node != nil && node.Host.ID() == itemBeingSkipped.StoredBy {
				if rb := node.RingBuffer(); rb != nil {
					// The stream was interrupted. Its ring buffer's nextSeq is the
					// sequence number that the *next* distinct stream should start with.
					finishedStreamNextSeq := rb.GetNextSeq()
					qc.UpdateGlobalMediaSequence(finishedStreamNextSeq)
					log.Printf("QueueController: Updated global media sequence to %d after stopping stream for %s (skipped by PREVIOUS)", qc.currentMediaSequence, itemBeingSkipped.FilePath)
				}
			}
		}

		newHeadItem, rewound := qc.q.RewindCursor()
		if rewound {
			madeChange = true
			log.Printf("QueueController: Rewound cursor for SKIP_PREV cmd. New cursor: %d, Item: %s", qc.q.GetCursor(), newHeadItem.FilePath)
		} else {
			log.Printf("QueueController: SKIP_PREV cmd, but cursor did not rewind (at start or empty). Cursor: %d. No change.", qc.q.GetCursor())
		}

	case p2ppb.QueueCmd_SKIP_NEXT: // User-initiated skip
		// Permission already checked.
		// Stop the current encoder *before* advancing the cursor.
		log.Printf("QueueController: Processing SKIP_NEXT command from %s. Current cursor: %d", sender, qc.q.GetCursor())

		itemBeingSkipped, wasPlaying := qc.q.Head()
		node.StopCurrentEncoder()

		if wasPlaying {
			if node != nil && node.Host.ID() == itemBeingSkipped.StoredBy {
				if rb := node.RingBuffer(); rb != nil {
					finishedStreamNextSeq := rb.GetNextSeq()
					qc.UpdateGlobalMediaSequence(finishedStreamNextSeq)
					log.Printf("QueueController: Updated global media sequence to %d after stopping stream for %s (skipped by NEXT)", qc.currentMediaSequence, itemBeingSkipped.FilePath)
				}
			}
		}

		newHeadItemAfterSkip, advancedSkip := qc.q.AdvanceCursor()
		if advancedSkip {
			madeChange = true
			log.Printf("QueueController: Advanced cursor for SKIP_NEXT. New cursor: %d, Item: %s", qc.q.GetCursor(), newHeadItemAfterSkip.FilePath)
		} else {
			log.Printf("QueueController: SKIP_NEXT called, but cursor did not advance (at end or empty). Cursor: %d. No change.", qc.q.GetCursor())
		}
	default:
		return fmt.Errorf("unknown QueueCmd type: %v", cmd.Type)
	}

	if madeChange {
		newHeadAfterChange, newHeadOk := qc.q.Head()
		logMsg := fmt.Sprintf("QueueController: Queue changed due to %s cmd from %s.", cmd.Type, sender)
		if headExistedBeforeChange {
			logMsg += fmt.Sprintf(" Old head: %s.", currentHeadBeforeChange.FilePath)
		}
		if newHeadOk {
			logMsg += fmt.Sprintf(" New head: %s.", newHeadAfterChange.FilePath)
		}
		logMsg += fmt.Sprintf(" Old cursor: %d, New cursor: %d.", cursorBeforeChange, qc.q.GetCursor())
		log.Println(logMsg)
		snapshotMsg := qc.Snapshot() // Get the *clientpb.ServerMsg
		// // Add HLC to the QueueUpdate payload itself
		// if quPayload, ok := snapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
		// 	quPayload.QueueUpdate.HlcTs = common.GetCurrentHLC() // Set HLC for the update event
		// }

		if hub != nil {
			hub.Broadcast(snapshotMsg)
		}
		if ctrlTopic != nil {
			if marshalledServerMsg, err := proto.Marshal(snapshotMsg); err != nil {
				log.Printf("[gossip QC.Handle] marshal ServerMsg{QueueUpdate} failed: %v", err)
			} else if err := ctrlTopic.Publish(ctx, marshalledServerMsg); err != nil {
				if ctx.Err() != nil {
					log.Printf("[gossip QC.Handle] context error during publish ServerMsg{QueueUpdate}: %v", ctx.Err())
				} else {
					log.Printf("[gossip QC.Handle] publish ServerMsg{QueueUpdate} failed: %v", err)
				}
			}
		}
	}

	// Always call ReactToQueueUpdate, even if no change from this specific command,
	// as other state (like permissions) might have changed, or an empty queue still needs handling.
	node.ReactToQueueUpdate()
	return nil
}

// ApplyUpdate replaces the entire local queue state with the items from the received update.
// It enforces RBAC: the sender must have PermQueue to update the queue.
func (qc *QueueController) ApplyUpdate(update *p2ppb.QueueUpdate, from peer.ID, localHub *Hub, node *Node, rm *roles.RoleManager) error {
	perms := rm.GetPermissionsForPeer(from)
	if !perms.Has(roles.PermQueue) {
		return fmt.Errorf("ApplyUpdate: permission denied. Peer %s lacks PermQueue", from)
	}

	// TODO: Implement HLC check for the update message itself (update.HlcTs)
	//       against a locally stored HLC for the last applied queue update.
	//       For now, always apply.
	log.Printf("QueueController.ApplyUpdate: Applying full queue update from %s with HLC %d. Items: %d", from, update.HlcTs, len(update.Items))

	newItems := make([]QueueItem, 0, len(update.Items))
	for _, pbItem := range update.Items {
		storedByID, errStored := peer.Decode(pbItem.StoredBy)
		if errStored != nil {
			log.Printf("ApplyUpdate: invalid StoredBy peer ID '%s': %v", pbItem.StoredBy, errStored)
			continue
		}
		addedByID, errAdded := peer.Decode(pbItem.AddedBy)
		if errAdded != nil {
			log.Printf("ApplyUpdate: invalid AddedBy peer ID '%s': %v", pbItem.AddedBy, errAdded)
			continue
		}
		newItems = append(newItems, QueueItem{
			FilePath: pbItem.FilePath,
			StoredBy: storedByID,
			AddedBy:  addedByID,
			FirstSeq: pbItem.FirstSeq,
			NumSegs:  pbItem.NumSegs,
			HlcTs:    pbItem.HlcTs,
		})
	}
	qc.q.ReplaceItems(newItems)

	// if localHub != nil {
	// 	snapshotMsg := qc.Snapshot()
	// 	// Update the HLC on the snapshot payload to reflect this event's time
	// 	if quPayload, ok := snapshotMsg.GetPayload().(*clientpb.ServerMsg_QueueUpdate); ok {
	// 		quPayload.QueueUpdate.HlcTs = common.GetCurrentHLC()
	// 	}
	// 	localHub.Broadcast(snapshotMsg)
	// }
	// qc.q.SetCursor(...) // TODO: If the update also implies a cursor change from gossip, handle it.
	// For now, assume gossip only syncs item list, local events (NEXT) change cursor.

	// Broadcast the newly applied state to local clients.
	// Snapshot already sets HLC.
	if localHub != nil {
		localHub.Broadcast(qc.Snapshot())
	} else if qc.hubRef != nil { // Use internal ref if specific one not passed
		qc.hubRef.Broadcast(qc.Snapshot())
	}

	if node != nil {
		node.ReactToQueueUpdate()
	} else if qc.nodeRef != nil { // Use internal ref
		qc.nodeRef.ReactToQueueUpdate()
	}
	return nil
}
