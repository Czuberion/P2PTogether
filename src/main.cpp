#include "gui/app.h"
#include "p2p/peer.h"
#include <QDebug>  // For qWarning if peerId is empty
#include <clocale> // Required for setlocale

int main(int argc, char* argv[]) {
    std::string peerId = "defaultPeerId"; // Provide a default or generate one
    // TODO: Add proper peer ID generation or retrieval logic here
    if (peerId.empty()) {
        qWarning() << "Peer ID is empty, using default. Pipe creation might "
                      "fail or collide.";
        peerId = "errorPeerId";
    }

    P2P::Peer peer(peerId); // Use the generated/retrieved ID
    gui::runGUI(&peer);
    return 0;
}