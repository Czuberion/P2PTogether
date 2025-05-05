package p2p

import (
	"reflect"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"

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
