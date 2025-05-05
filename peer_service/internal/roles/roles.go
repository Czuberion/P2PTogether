package roles

import (
	"fmt"
	"strings"
	"sync"
)

// Permission represents peer permissions in the network
type Permission int

const (
	// Interaction permissions

	PermView Permission = 1 << iota // Permits viewing the stream
	PermChat                        // Permits sending messages in the chat

	// Moderation permissions

	PermKickUser        // Permits kicking a user from the session
	PermBanUser         // Permits banning a user from the session. Makes them unable to rejoin
	PermModerateChat    // Permits moderating the chat, including deleting messages and muting users
	PermAddRemoveRoles  // Permits adding or removing roles session-wide
	PermManageUserRoles // Permits managing roles of users, including granting or revoking roles

	// Streaming permissions

	PermStream    // Permits streaming the content
	PermPlayPause // Permits playing or pausing the stream
	PermSeek      // Permits seeking within the stream - changing the current position of playback
	PermSetSpeed  // Permits changing the playback speed of the stream
	PermQueue     // Permits adding, removing, and clearing items from the queue

	PermAll = (1 << iota) - 1 // needs to be the last one
)

// Role defines a peer's role with a name and associated permissions
type Role struct {
	Name        string
	Permissions Permission
}

// defaultRoles defines all predefined roles with their associated permissions
var defaultRoles = []*Role{
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

var (
	// AllRoles maps canonical role names → Role.
	// Populated in init() for O(1) look‑ups.
	AllRoles   map[string]*Role
	AllRolesMu sync.RWMutex
)

func init() {
	AllRolesMu.Lock()
	defer AllRolesMu.Unlock()
	AllRoles = make(map[string]*Role, len(defaultRoles))
	for _, r := range defaultRoles {
		AllRoles[strings.ToLower(r.Name)] = r // normalise key
	}
}

// PermissionsForRoles returns the OR‑ed Permission mask for the given roles.
func PermissionsForRoles(roles ...*Role) Permission {
	var p Permission
	for _, role := range roles {
		p |= role.Permissions
	}
	return p
}

// PermissionsForRolesStr returns the OR‑ed Permission mask for the given role names.
// Unknown names yield an error.
func PermissionsForRolesStr(roleNames ...string) (Permission, error) {
	var p Permission
	AllRolesMu.RLock()
	defer AllRolesMu.RUnlock()
	for _, raw := range roleNames {
		if role, ok := AllRoles[strings.ToLower(strings.TrimSpace(raw))]; ok {
			p |= role.Permissions
		} else {
			return 0, fmt.Errorf("invalid role: %s", raw)
		}
	}
	return p, nil
}

// HasPermission checks if the given permission is present in the permission mask
func HasPermission(required Permission, perms Permission) bool {
	return perms&required != 0
}

// HasPermissionFromRoles checks if any of the roles contain the required permission
func HasPermissionFromRoles(required Permission, roles ...*Role) bool {
	return HasPermission(required, PermissionsForRoles(roles...))
}

// ParseRoles parses a comma-separated string of role names and returns the corresponding Role structs
func ParseRoles(roleStr string) ([]*Role, error) {
	var out []*Role
	AllRolesMu.RLock()
	defer AllRolesMu.RUnlock()
	for _, raw := range strings.Split(roleStr, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if role, ok := AllRoles[name]; ok {
			out = append(out, role)
		} else {
			return nil, fmt.Errorf("invalid role: %s", raw)
		}
	}
	return out, nil
}
