package roles

import (
	"fmt"
	"strings"
)

// Permission represents peer permissions in the network
type Permission int

const (
	// Interaction permissions

	View Permission = 1 << iota // Permits viewing the stream
	Chat                        // Permits sending messages in the chat

	// Moderation permissions

	KickUser        // Permits kicking a user from the session
	BanUser         // Permits banning a user from the session. Makes them unable to rejoin
	ModerateChat    // Permits moderating the chat, including deleting messages and muting users
	AddRemoveRoles  // Permits adding or removing roles session-wide
	ManageUserRoles // Permits managing roles of users, including granting or revoking roles

	// Streaming permissions

	Stream     // Permits streaming the content
	PlayPause  // Permits playing or pausing the stream
	Seek       // Permits seeking within the stream - changing the current position of playback
	SetSpeed   // Permits changing the playback speed of the stream
	Queue      // Permits adding and removing items from the queue
	ClearQueue // Permits clearing the queue

	All = (1 << iota) - 1 // needs to be the last one
)

// Role defines a peer's role with a name and associated permissions
type Role struct {
	Name        string
	Permissions Permission
}

// AllRoles defines all predefined roles with their associated permissions
var AllRoles = []Role{
	{
		Name:        "Admin",
		Permissions: All,
	},
	{
		Name:        "Moderator",
		Permissions: KickUser | BanUser | ModerateChat | AddRemoveRoles | ManageUserRoles,
	},
	{
		Name:        "Streamer",
		Permissions: Stream | PlayPause | Seek | SetSpeed | Queue | ClearQueue,
	},
	{
		Name:        "Viewer",
		Permissions: View | Chat,
	},
	// Add more predefined roles here as needed
}

// CalculateTotalPermissions computes the combined permissions from a slice of roles.
func CalculateTotalPermissions(roles []Role) Permission {
	var totalPermissions Permission
	for _, role := range roles {
		totalPermissions |= role.Permissions
	}
	return totalPermissions
}

// HasPermission checks if any of the roles contain the required permission
func HasPermission(required Permission, roles []Role) bool {
	return CalculateTotalPermissions(roles)&required != 0
}

// ParseRoles parses a comma-separated string of role names and returns the corresponding Role structs
func ParseRoles(roleStr string) ([]Role, error) {
	var roles []Role
	roleNames := strings.Split(roleStr, ",")
	for _, name := range roleNames {
		name = strings.TrimSpace(name)
		found := false
		for _, role := range AllRoles {
			if role.Name == name {
				roles = append(roles, role)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("invalid role: %s", name)
		}
	}
	return roles, nil
}
