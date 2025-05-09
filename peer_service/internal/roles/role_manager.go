// peer_service/internal/roles/role_manager.go
package roles

import (
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"

	"peer_service/internal/common"
	p2ppb "peer_service/proto/p2p"
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

	// assignments maps peer ID to their assigned roles and the HLC time of that assignment.
	assignments   map[peer.ID]peerAssignmentState
	assignmentsMu sync.RWMutex
}

// peerAssignmentState stores the roles and the HLC timestamp of their last update.
type peerAssignmentState struct {
	roleNames []string
	hlcTs     int64
}

// NewRoleManager creates a new manager, initializing definitions with defaults.
func NewRoleManager() *RoleManager {
	rm := &RoleManager{
		definitions: make(map[string]*Role),
		assignments: make(map[peer.ID]peerAssignmentState),
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

// GetAssignedRoles returns a slice of role names assigned to the given peer and
// the HLC timestamp of that assignment.
// Returns an empty slice and 0 if the peer has no roles assigned.
func (rm *RoleManager) GetAssignedRoles(peerID peer.ID) ([]string, int64) {
	rm.assignmentsMu.RLock()
	defer rm.assignmentsMu.RUnlock()

	state, exists := rm.assignments[peerID]
	if !exists {
		return []string{}, 0 // Return empty slice, not nil
	}
	// Return a copy of the string slice
	assignedCopy := make([]string, len(state.roleNames))
	copy(assignedCopy, state.roleNames)
	return assignedCopy, state.hlcTs
}

// GetPermissionsForPeer calculates the combined permission mask for a peer
// based on their currently assigned roles and the current role definitions.
func (rm *RoleManager) GetPermissionsForPeer(peerID peer.ID) Permission {
	assignedNames, _ := rm.GetAssignedRoles(peerID) // Ignore HLC for permission calculation itself

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
// Existing roles for the peer are overwritten. Returns a PeerRoleAssignment
// message suitable for broadcasting if the assignment actually changed, otherwise nil.
func (rm *RoleManager) SetPeerRoles(targetPeer peer.ID, roleNames []string) (*p2ppb.PeerRoleAssignment, error) {
	normalizedNames := make([]string, 0, len(roleNames))

	// Validate and normalize role names against current definitions
	rm.definitionsMu.RLock()
	for _, name := range roleNames {
		normName := strings.ToLower(strings.TrimSpace(name))
		if _, exists := rm.definitions[normName]; !exists {
			rm.definitionsMu.RUnlock()
			return nil, fmt.Errorf("role '%s' not defined", name)
		}
		if normName != "" {
			normalizedNames = append(normalizedNames, normName)
		}
	}
	rm.definitionsMu.RUnlock()

	// Check if the assignment actually changes to avoid unnecessary broadcasts
	rm.assignmentsMu.Lock()
	currentState, exists := rm.assignments[targetPeer]
	changed := !exists || !slicesEqual(currentState.roleNames, normalizedNames)

	if changed {
		newHlcTs := common.GetCurrentHLC() // Generate HLC timestamp for this specific assignment change
		if len(normalizedNames) > 0 {
			namesCopy := make([]string, len(normalizedNames))
			copy(namesCopy, normalizedNames)
			rm.assignments[targetPeer] = peerAssignmentState{
				roleNames: namesCopy,
				hlcTs:     newHlcTs,
			}
		} else {
			delete(rm.assignments, targetPeer)
		}
		rm.assignmentsMu.Unlock() // Unlock early if changed

		return &p2ppb.PeerRoleAssignment{ // HlcTs is now set based on the assignment time
			PeerId:            targetPeer.String(),
			AssignedRoleNames: normalizedNames,
			HlcTs:             newHlcTs,
		}, nil
	}
	rm.assignmentsMu.Unlock()
	return nil, nil // No change, nothing to broadcast
}

// GetAllAssignments returns a snapshot of all current peer role assignments.
// Each element in the returned slice is a PeerRoleAssignment message payload,
// including the HLC timestamp of its last update.
func (rm *RoleManager) GetAllAssignments() []*p2ppb.PeerRoleAssignment {
	rm.assignmentsMu.RLock()
	defer rm.assignmentsMu.RUnlock()

	allAssignments := make([]*p2ppb.PeerRoleAssignment, 0, len(rm.assignments))
	for peerID, state := range rm.assignments {
		namesCopy := make([]string, len(state.roleNames))
		copy(namesCopy, state.roleNames)

		allAssignments = append(allAssignments, &p2ppb.PeerRoleAssignment{
			PeerId:            peerID.String(),
			AssignedRoleNames: namesCopy,
			HlcTs:             state.hlcTs, // Use the stored HLC timestamp for this assignment
		})
	}
	return allAssignments
}

// Helper function to compare two string slices (order matters for this check)
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
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
// Returns a RoleDefinitionsUpdate message if the definitions changed.
func (rm *RoleManager) UpdateDefinition(name string, permissions Permission) (*p2ppb.RoleDefinitionsUpdate, error) {
	rm.definitionsMu.Lock()
	defer rm.definitionsMu.Unlock()

	normalizedName := strings.ToLower(strings.TrimSpace(name))
	if normalizedName == "" {
		return nil, fmt.Errorf("role name cannot be empty")
	}

	// Overwrite or add
	rm.definitions[normalizedName] = &Role{
		Name:        name, // Store original casing? Or normalized? Using original for display.
		Permissions: permissions,
	}

	// Prepare RoleDefinitionsUpdate message with all current definitions
	allDefs := make([]*p2ppb.RoleDefinition, 0, len(rm.definitions))
	for _, rolePtr := range rm.definitions {
		allDefs = append(allDefs, &p2ppb.RoleDefinition{Name: rolePtr.Name, Permissions: uint32(rolePtr.Permissions)})
	}
	// Timestamp will be set by the caller (e.g., gRPC handler) when the event is processed
	return &p2ppb.RoleDefinitionsUpdate{Definitions: allDefs}, nil
}

// RemoveDefinition removes a role definition.
// It also removes the specified role from all peer assignments.
// Returns RoleDefinitionsUpdate and a slice of PeerRoleAssignment for affected peers.
func (rm *RoleManager) RemoveDefinition(roleName string) (*p2ppb.RoleDefinitionsUpdate, []*p2ppb.PeerRoleAssignment, error) {
	rm.definitionsMu.Lock()

	normalizedName := strings.ToLower(strings.TrimSpace(roleName))
	if _, exists := rm.definitions[normalizedName]; !exists {
		rm.definitionsMu.Unlock()
		return nil, nil, fmt.Errorf("role '%s' not defined", roleName)
	}

	// Delete the definition
	delete(rm.definitions, normalizedName)

	// Prepare RoleDefinitionsUpdate message
	allDefsPb := make([]*p2ppb.RoleDefinition, 0, len(rm.definitions))
	for _, rolePtr := range rm.definitions {
		allDefsPb = append(allDefsPb, &p2ppb.RoleDefinition{Name: rolePtr.Name, Permissions: uint32(rolePtr.Permissions)})
	}
	// Timestamp will be set by the caller
	definitionsUpdateMsg := &p2ppb.RoleDefinitionsUpdate{Definitions: allDefsPb}
	rm.definitionsMu.Unlock()

	// Now clean up assignments
	// This needs to be done carefully with locking.
	rm.assignmentsMu.Lock()
	defer rm.assignmentsMu.Unlock()

	affectedAssignments := make([]*p2ppb.PeerRoleAssignment, 0)

	for peerID, assignmentState := range rm.assignments {
		newNames := make([]string, 0, len(assignmentState.roleNames))
		roleWasRemoved := false
		for _, assignedName := range assignmentState.roleNames {
			if assignedName != normalizedName {
				newNames = append(newNames, assignedName)
			} else {
				roleWasRemoved = true
			}
		}
		// If the list changed and is now empty, delete the entry, otherwise update it
		if roleWasRemoved {
			newHlcForAssignment := common.GetCurrentHLC() // New HLC for the modified assignment
			if len(newNames) == 0 {
				delete(rm.assignments, peerID)
			} else {
				rm.assignments[peerID] = peerAssignmentState{ // Store as peerAssignmentState
					roleNames: newNames,
					hlcTs:     newHlcForAssignment,
				}
			}
			affectedAssignments = append(affectedAssignments, &p2ppb.PeerRoleAssignment{
				PeerId:            peerID.String(),
				AssignedRoleNames: newNames,
				HlcTs:             newHlcForAssignment, // Use the new HLC for this specific update
			})
		}
	}

	return definitionsUpdateMsg, affectedAssignments, nil
}
