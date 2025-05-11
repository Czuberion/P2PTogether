#include "peer.h"

namespace P2P {

Peer::Peer() : peerId("defaultPeerId"), isIdentityConfirmed(false) {
    roles.push_back(Role::Viewer);
}

Peer::Peer(const std::string& initialPeerId) :
    peerId(initialPeerId), isIdentityConfirmed(false) {
    if (initialPeerId.empty()) {
        this->peerId = "defaultPeerId"; // Ensure not empty
    }
    roles.push_back(Role::Viewer);
}

bool Peer::hasRole(Role role) const {
    for (const auto& r : roles) {
        if (r == role)
            return true;
    }
    return false;
}

void Peer::setTruePeerId(const std::string& trueId) {
    this->peerId              = trueId;
    this->isIdentityConfirmed = true;
    std::cout << "Peer identity confirmed: " << this->peerId << std::endl;
}

std::string Peer::getPeerId() const {
    return this->peerId;
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
