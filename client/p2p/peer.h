/*!
 * \file
 * \brief Dummy Peer class for GUI stubs. Real functionality is provided by the
 * Go daemon.
 *
 * This stub allows the GUI to compile and test role-based logic and session
 * management, but all real peer/network logic is implemented in the Peer
 * Service.
 *
 * \see PRD section 6
 * \see SRS 2.1
 */
#ifndef PEER_H
#define PEER_H

#include <iostream>
#include <string>
#include <vector>

namespace P2P {

enum class Role { Viewer, Streamer, Moderator, Admin };

class Peer {
public:
    const std::string peerId;

    Peer();
    Peer(const std::string& peerId);

    bool hasRole(Role role) const;
    void cleanup();
    bool startStreaming(const std::string& filePath);

    std::vector<Role> roles;
};

inline bool hasQueuePermission(const std::vector<Role>& roles) {
    for (auto r : roles) {
        if (r == Role::Streamer || r == Role::Moderator || r == Role::Admin)
            return true;
    }
    return false;
}

} // namespace P2P

#endif // PEER_H
