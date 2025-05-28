package roles

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
	PermInvite          // Permits viewing/copying the session invite code

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

// PermissionsForRoles returns the OR‑ed Permission mask for the given roles.
func PermissionsForRoles(roles ...*Role) Permission {
	var p Permission
	for _, role := range roles {
		p |= role.Permissions
	}
	return p
}

// HasPermission checks if the given permission is present in the permission mask
func HasPermission(required Permission, perms Permission) bool {
	return perms&required != 0
}

// HasPermissionFromRoles checks if any of the roles contain the required permission
// func HasPermissionFromRoles(required Permission, roles ...*Role) bool {
// 	return HasPermission(required, PermissionsForRoles(roles...))
// }

func (p Permission) Has(required Permission) bool {
	return p&required != 0
}
