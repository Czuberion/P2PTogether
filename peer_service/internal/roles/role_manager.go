// peer_service/internal/roles/role_manager.go
package roles

import (
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// defaultRoleDefinitions defines the built-in roles included when a RoleManager is created.
// This is kept unexported as the RoleManager owns the live definition state.
var defaultRoleDefinitions = []*Role{
	{
		Name:        "Admin",
		Permissions: PermAll,
	},
	{
		Name: "Moderator",
		Permissions: PermKickUser |
			PermBanUser |
			PermModerateChat |
			PermAddRemoveRoles |
			PermManageUserRoles,
	},
	{
		Name: "Streamer",
		Permissions: PermStream |
			PermPlayPause |
			PermSeek |
			PermSetSpeed |
			PermQueue,
	},
	{
		Name:        "Viewer",
		Permissions: PermView | PermChat,
	},
}

// RoleManager manages the dynamic state of role definitions and peer assignments.
type RoleManager struct {
	// definitions maps lower-case role name to its definition.
	definitions   map[string]*Role
	definitionsMu sync.RWMutex

	// assignments maps peer ID to a slice of lower-case role names.
	assignments   map[peer.ID][]string
	assignmentsMu sync.RWMutex
}

// NewRoleManager creates a new manager, initializing definitions with defaults.
func NewRoleManager() *RoleManager {
	rm := &RoleManager{
		definitions: make(map[string]*Role),
		assignments: make(map[peer.ID][]string),
	}

	// Populate initial definitions from defaults
	rm.definitionsMu.Lock()
	for _, defaultRole := range defaultRoleDefinitions {
		// Ensure we store a copy, not the original pointer
		roleCopy := *defaultRole
		normalizedName := strings.ToLower(roleCopy.Name)
		rm.definitions[normalizedName] = &roleCopy
	}
	rm.definitionsMu.Unlock()

	return rm
}

// GetDefinitions returns a slice containing value copies of all current role definitions.
func (rm *RoleManager) GetDefinitions() []Role {
	rm.definitionsMu.RLock()
	defer rm.definitionsMu.RUnlock()

	defs := make([]Role, 0, len(rm.definitions))
	for _, rolePtr := range rm.definitions {
		defs = append(defs, *rolePtr)
	}
	return defs
}

// GetDefinition returns a value copy of a specific role definition by name (case-insensitive).
func (rm *RoleManager) GetDefinition(roleName string) (Role, bool) {
	rm.definitionsMu.RLock()
	defer rm.definitionsMu.RUnlock()

	rolePtr, exists := rm.definitions[strings.ToLower(roleName)]
	if !exists {
		return Role{}, false // Return zero-value Role and false
	}
	return *rolePtr, true
}

// GetAssignedRoles returns a slice of role names assigned to the given peer.
// Returns an empty slice if the peer has no roles assigned.
func (rm *RoleManager) GetAssignedRoles(peerID peer.ID) []string {
	rm.assignmentsMu.RLock()
	defer rm.assignmentsMu.RUnlock()

	assigned, exists := rm.assignments[peerID]
	if !exists {
		return []string{} // Return empty slice, not nil
	}
	// Return a copy of the string slice
	assignedCopy := make([]string, len(assigned))
	copy(assignedCopy, assigned)
	return assignedCopy
}

// GetPermissionsForPeer calculates the combined permission mask for a peer
// based on their currently assigned roles and the current role definitions.
func (rm *RoleManager) GetPermissionsForPeer(peerID peer.ID) Permission {
	assignedNames := rm.GetAssignedRoles(peerID) // This already handles locking and copying

	var effectivePermissions Permission
	if len(assignedNames) == 0 {
		return effectivePermissions // No roles, no permissions
	}

	rm.definitionsMu.RLock()
	defer rm.definitionsMu.RUnlock()

	for _, roleName := range assignedNames {
		// roleName should already be lower-case if SetPeerRoles normalized it
		if rolePtr, exists := rm.definitions[roleName]; exists {
			effectivePermissions |= rolePtr.Permissions // Use permission from internal pointer
		}
	}

	return effectivePermissions
}

// SetPeerRoles assigns a specific list of roles to a target peer.
// It validates that all provided role names exist in the current definitions.
// Existing roles for the peer are overwritten.
func (rm *RoleManager) SetPeerRoles(targetPeer peer.ID, roleNames []string) error {
	normalizedNames := make([]string, 0, len(roleNames))

	// 1. Validate and normalize role names against current definitions
	rm.definitionsMu.RLock()
	for _, name := range roleNames {
		normName := strings.ToLower(strings.TrimSpace(name))
		if _, exists := rm.definitions[normName]; !exists {
			rm.definitionsMu.RUnlock()
			return fmt.Errorf("role '%s' not defined", name)
		}
		if normName != "" {
			normalizedNames = append(normalizedNames, normName)
		}
	}
	rm.definitionsMu.RUnlock()

	// 2. Update assignments map
	rm.assignmentsMu.Lock()
	if len(normalizedNames) > 0 {
		namesCopy := make([]string, len(normalizedNames))
		copy(namesCopy, normalizedNames)
		rm.assignments[targetPeer] = namesCopy
	} else {
		delete(rm.assignments, targetPeer)
	}
	rm.assignmentsMu.Unlock()

	// TODO: Trigger broadcast
	// This function would need access to Hub and Topic, or return data to the caller
	// Example: rm.broadcastAssignmentUpdate(targetPeer, normalizedNames)

	return nil
}

// ParseRoleNames validates a comma-separated string of role names against
// the currently managed definitions and returns the corresponding Role structs
// (as value copies).
func (rm *RoleManager) ParseRoleNames(roleStr string) ([]Role, error) {
	var out []Role
	rawNames := strings.Split(roleStr, ",")

	rm.definitionsMu.RLock()
	defer rm.definitionsMu.RUnlock()

	for _, raw := range rawNames {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if rolePtr, ok := rm.definitions[name]; ok {
			out = append(out, *rolePtr) // Append the value copy
		} else {
			return nil, fmt.Errorf("role not defined: '%s'", raw)
		}
	}
	return out, nil
}

// --- Future Methods (Example Stubs) ---

// NOTE: When broadcasting updates after definition changes (Update/Remove),
// consider also broadcasting affected PeerRoleAssignments if permissions change significantly,
// or rely on clients recalculating permissions based on the new definitions.
// Broadcasting RoleDefinitionsUpdate is essential.

// UpdateDefinition adds or updates a role definition.
func (rm *RoleManager) UpdateDefinition(name string, permissions Permission) error {
	rm.definitionsMu.Lock()
	defer rm.definitionsMu.Unlock()

	normalizedName := strings.ToLower(strings.TrimSpace(name))
	if normalizedName == "" {
		return fmt.Errorf("role name cannot be empty")
	}

	// Store internal pointer
	rm.definitions[normalizedName] = &Role{
		Name:        name, // Storing original casing for Name might be preferable
		Permissions: permissions,
	}

	// TODO: Trigger broadcast
	// Example: rm.broadcastDefinitionsUpdate()

	return nil
}

// RemoveDefinition removes a role definition.
func (rm *RoleManager) RemoveDefinition(roleName string) error {
	rm.definitionsMu.Lock()
	defer rm.definitionsMu.Unlock()

	normalizedName := strings.ToLower(strings.TrimSpace(roleName))
	if _, exists := rm.definitions[normalizedName]; !exists {
		return fmt.Errorf("role '%s' not defined", roleName)
	}

	delete(rm.definitions, normalizedName)

	// Now clean up assignments
	rm.assignmentsMu.Lock()
	defer rm.assignmentsMu.Unlock()

	peersToUpdate := []peer.ID{} // Keep track of peers whose roles changed
	for peerID, names := range rm.assignments {
		newNames := make([]string, 0, len(names))
		roleWasRemoved := false
		for _, assignedName := range names {
			if assignedName != normalizedName {
				newNames = append(newNames, assignedName)
			} else {
				roleWasRemoved = true
			}
		}
		// If the list changed and is now empty, delete the entry, otherwise update it
		if roleWasRemoved {
			peersToUpdate = append(peersToUpdate, peerID)
			if len(newNames) == 0 {
				delete(rm.assignments, peerID)
			} else {
				rm.assignments[peerID] = newNames
			}
		}
	}

	// TODO: Trigger broadcast
	// Example: rm.broadcastDefinitionsUpdate()
	// Example: foreach peerID in peersToUpdate: rm.broadcastAssignmentUpdate(peerID, rm.assignments[peerID])

	return nil
}
