package session

import (
	"fmt"
	"log"
	"sync"

	"peer_service/internal/roles"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/peer"
)

// SessionManager manages active sessions.
type SessionManager struct {
	// sessions maps SessionID to the Session object.
	sessions map[string]*Session
	mu       sync.RWMutex

	roleManager *roles.RoleManager // To assign roles to peers within sessions
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager(rm *roles.RoleManager) *SessionManager {
	if rm == nil {
		// This is a critical dependency.
		// For a real application, either ensure it's always provided or handle this more robustly.
		log.Println("Warning: SessionManager initialized with a nil RoleManager. Role assignments will not function correctly.")
	}
	return &SessionManager{
		sessions:    make(map[string]*Session),
		roleManager: rm,
	}
}

// CreateSession creates a new session, making the adminPeerID its administrator.
// It assigns the "Admin" role to the creator within this session's context.
func (sm *SessionManager) CreateSession(adminPeerID peer.ID, adminUsername string, sessionName string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessionID := uuid.NewString() // Generate a Version 4 UUID string.

	if _, exists := sm.sessions[sessionID]; exists {
		// Extremely unlikely with UUIDs, but good practice.
		return nil, fmt.Errorf("session ID %s already exists (collision)", sessionID)
	}

	newSession := &Session{
		ID:          sessionID,
		AdminPeerID: adminPeerID,
		Name:        sessionName,
		IsPrivate:   false,     // Default for now
		InviteCode:  sessionID, // Option B: InviteCode is the SessionID
		Members:     make(map[peer.ID]UserInfo),
		Banned:      make(map[peer.ID]BanInfo),
	}

	// Add admin as the first member
	adminRoles := []string{"Admin"} // Default role for session creator
	adminInfo := UserInfo{
		Username: adminUsername,
		Roles:    adminRoles,
		PeerID:   adminPeerID,
	}
	newSession.Members[adminPeerID] = adminInfo
	sm.sessions[sessionID] = newSession

	// Also, update the global RoleManager about this peer's roles *for this specific peer service instance*.
	// This is important if the GUI relies on RoleManager for the local peer's permissions.
	if sm.roleManager != nil {
		// Note: SetPeerRoles might broadcast. If creating many sessions rapidly, this could be noisy.
		// However, session creation is typically a user-driven, infrequent event.
		_, err := sm.roleManager.SetPeerRoles(adminPeerID, adminRoles) // Assign "Admin" role
		if err != nil {
			// Log the error but don't fail session creation. The session exists,
			// but the role might not be reflected immediately in RoleManager's broadcasts
			// if SetPeerRoles had an issue or didn't cause a "change" from its perspective.
			log.Printf("Warning: Session %s created, but RoleManager.SetPeerRoles for Admin %s encountered: %v. The session's internal admin is set.", sessionID, adminPeerID, err)
		}
	}

	log.Printf("SessionManager: Session %s created. Admin: %s (%s). Invite Code: %s", sessionID, adminPeerID, adminUsername, newSession.InviteCode)
	return newSession, nil
}

// GetSession retrieves a session by its ID.
func (sm *SessionManager) GetSession(sessionID string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[sessionID]
	return s, ok
}

// AddPeerToSession adds a peer to an existing session with specified initial roles and username.
// This is typically called after a successful join authentication.
func (sm *SessionManager) AddPeerToSession(sessionID string, peerID peer.ID, username string, initialRoles []string) (*Session, UserInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, UserInfo{}, fmt.Errorf("session %s not found", sessionID)
	}

	if _, peerExists := session.Members[peerID]; peerExists {
		// Handle re-join logic if necessary. For now, treat as error or update.
		// This could mean updating username/roles or simply acknowledging.
		// Let's return the existing info and denote no "new" add.
		log.Printf("SessionManager: Peer %s already in session %s. Returning existing info.", peerID, sessionID)
		return session, session.Members[peerID], nil // Not an error, but indicates already a member
	}

	if len(initialRoles) == 0 {
		initialRoles = []string{"Viewer"} // Default to "Viewer" if not specified
	}

	newUserInfo := UserInfo{
		Username: username,
		Roles:    initialRoles,
		PeerID:   peerID,
	}
	session.Members[peerID] = newUserInfo

	// Update the global RoleManager for this newly joined peer.
	if sm.roleManager != nil {
		// This will trigger a broadcast if roles actually changed from RoleManager's perspective
		_, err := sm.roleManager.SetPeerRoles(peerID, initialRoles)
		if err != nil {
			log.Printf("Warning: Peer %s added to session %s, but RoleManager.SetPeerRoles encountered: %v.", peerID, sessionID, err)
		}
	}

	log.Printf("SessionManager: Peer %s (%s) added to session %s with roles %v.", peerID, username, sessionID, initialRoles)
	return session, newUserInfo, nil
}

// RemovePeerFromSession removes a peer from an existing session.
func (sm *SessionManager) RemovePeerFromSession(sessionID string, peerID peer.ID) (UserInfo, bool, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return UserInfo{}, false, fmt.Errorf("session %s not found", sessionID)
	}

	userInfo, peerExists := session.Members[peerID]
	if !peerExists {
		return UserInfo{}, false, nil
	}

	delete(session.Members, peerID)
	log.Printf("SessionManager: Peer %s removed from session %s.", peerID, sessionID)
	return userInfo, true, nil
}

// IsPeerBanned checks if a peer is banned from a session.
func (sm *SessionManager) IsPeerBanned(sessionID string, peerID peer.ID) (bool, BanInfo) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return false, BanInfo{}
	}

	banInfo, banned := session.Banned[peerID]
	return banned, banInfo
}

// BanPeer bans a peer from a session and removes them if present.
func (sm *SessionManager) BanPeer(sessionID string, peerID peer.ID, bannedBy peer.ID, reason string, bannedAt int64) (BanInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return BanInfo{}, fmt.Errorf("session %s not found", sessionID)
	}

	banInfo := BanInfo{
		PeerID:   peerID,
		BannedBy: bannedBy,
		Reason:   reason,
		BannedAt: bannedAt,
	}

	if memberInfo, ok := session.Members[peerID]; ok {
		banInfo.Username = memberInfo.Username
		delete(session.Members, peerID)
	}

	session.Banned[peerID] = banInfo
	log.Printf("SessionManager: Peer %s banned from session %s by %s. Reason: %s", peerID, sessionID, bannedBy, reason)
	return banInfo, nil
}

// IsPeerInSession checks if a peer is a member of a given session.
func (sm *SessionManager) IsPeerInSession(sessionID string, peerID peer.ID) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return false
	}
	_, peerExists := session.Members[peerID]
	return peerExists
}

// GetSessionMembers returns a list of UserInfo for all members in a session.
func (sm *SessionManager) GetSessionMembers(sessionID string) ([]UserInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	members := make([]UserInfo, 0, len(session.Members))
	for _, userInfo := range session.Members {
		members = append(members, userInfo)
	}
	return members, nil
}
