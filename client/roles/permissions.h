#pragma once

#include <QtGlobal>

namespace P2P {
namespace Roles {

// These values MUST exactly match the const definitions in
// peer_service/internal/roles/roles.go
enum Permission : quint32 {
    // Interaction permissions
    PermView = 1 << 0, // Permits viewing the stream
    PermChat = 1 << 1, // Permits sending messages in the chat

    // Moderation permissions
    PermKickUser     = 1 << 2, // Permits kicking a user from the session
    PermBanUser      = 1 << 3, // Permits banning a user from the session.
    PermModerateChat = 1 << 4, // Permits moderating the chat
    PermAddRemoveRoles =
        1 << 5, // Permits adding or removing role definitions session-wide
    PermManageUserRoles = 1 << 6, // Permits managing roles of users

    // Streaming permissions
    PermStream    = 1 << 7,  // Permits streaming the content
    PermPlayPause = 1 << 8,  // Permits playing or pausing the stream
    PermSeek      = 1 << 9,  // Permits seeking within the stream
    PermSetSpeed  = 1 << 10, // Permits changing the playback speed
    PermQueue =
        1 << 11, // Permits adding, removing, and clearing items from the queue

    // PermAll should be the last one, calculated to include all previous bits
    // Ensure this matches the Go calculation if more permissions are added.
    // Current Go: (1 << iota) - 1, if PermQueue is iota=11, then PermAll is
    // (1<<12)-1
    PermAll = (1 << 12) - 1
};

// Helper to check if a set of permissions includes a required permission
inline bool hasPermission(quint32 currentPermissions,
                          Permission requiredPermission) {
    return (currentPermissions & static_cast<quint32>(requiredPermission)) != 0;
}

} // namespace Roles
} // namespace P2P