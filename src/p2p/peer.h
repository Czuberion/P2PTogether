#ifndef PEER_H
#define PEER_H

#include <iostream>
#include <string>
#include <vector>

namespace P2P {

enum class Role { Viewer, Streamer };

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

} // namespace P2P

#endif // PEER_H
