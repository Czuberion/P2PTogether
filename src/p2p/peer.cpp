#include "peer.h"

namespace P2P {

Peer::Peer() {
    roles.push_back(Role::Viewer); // Default role is Viewer
}

Peer::Peer(const std::string& peerId) : peerId(peerId) {
    roles.push_back(Role::Viewer);
}

bool Peer::hasRole(Role role) const {
    for (const auto& r : roles) {
        if (r == role)
            return true;
    }
    return false;
}

void Peer::cleanup() {
    std::cout << "Cleaning up resources..." << std::endl;
}

bool Peer::startStreaming(const std::string& filePath) {
    if (!hasRole(Role::Streamer)) {
        std::cerr << "Peer does not have streamer role" << std::endl;
        return false;
    }
    std::cout << "Starting to stream: " << filePath << std::endl;
    return true;
}

} // namespace P2P
