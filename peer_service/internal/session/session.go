package session

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// UserInfo stores information about a user within a session.
type UserInfo struct {
	Username string
	Roles    []string // List of role names (e.g., "Admin", "Viewer")
	PeerID   peer.ID  // The libp2p PeerID of the user
}

// Session represents an active P2PTogether session.
type Session struct {
	ID          string // Unique Session ID (UUID v4 string)
	AdminPeerID peer.ID
	Name        string // Optional, user-defined session name
	IsPrivate   bool   // For future use (F-M7 AES encryption)
	InviteCode  string // For Option B, this will be the same as SessionID

	// Members tracks authenticated peers in the session and their info.
	// Keyed by peer.ID.String() for easier map keying if direct peer.ID causes issues,
	// but peer.ID itself is generally fine as a map key.
	Members map[peer.ID]UserInfo

	// mu sync.RWMutex // Use if Session fields (like Members) are modified directly by multiple goroutines.
	// For now, SessionManager will manage concurrent access to its map of sessions, and
	// modifications to a Session's Members map will be done via SessionManager methods which are locked.
}
