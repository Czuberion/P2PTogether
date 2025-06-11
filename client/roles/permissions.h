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
    PermInvite = 1 << 7, // Permits viewing/copying the session invite code

    // Streaming permissions
    PermStream    = 1 << 8,  // Permits streaming the content
    PermPlayPause = 1 << 9,  // Permits playing or pausing the stream
    PermSeek      = 1 << 10, // Permits seeking within the stream
    PermSetSpeed  = 1 << 11, // Permits changing the playback speed
    PermQueue =
        1 << 12, // Permits adding, removing, and clearing items from the queue

    // PermAll should be the last one, calculated to include all previous bits
    // Ensure this matches the Go calculation if more permissions are added.
    PermAll = (1 << 13) - 1
};

inline const QMap<Permission, QString> getPermissionDescriptions() {
    static const QMap<Permission, QString> permissionMap = {
        {PermView, "View Stream"},
        {PermChat, "Chat"},
        {PermKickUser, "Kick Users"},
        {PermBanUser, "Ban Users"},
        {PermModerateChat, "Moderate Chat"},
        {PermAddRemoveRoles, "Manage Roles"},
        {PermManageUserRoles, "Manage User Roles"},
        {PermInvite, "Invite Users"},
        {PermStream, "Stream Content"},
        {PermPlayPause, "Play/Pause"},
        {PermSeek, "Seek"},
        {PermSetSpeed, "Set Speed"},
        {PermQueue, "Manage Queue"},
    };
    return permissionMap;
}

// Helper to check if a set of permissions includes a required permission
inline bool hasPermission(quint32 currentPermissions,
                          Permission requiredPermission) {
    return (currentPermissions & static_cast<quint32>(requiredPermission)) != 0;
}

} // namespace Roles
} // namespace P2P