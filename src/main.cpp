/*!
 * \file main.cpp
 * \brief Entry point for the P2PTogether application.
 * \details This file contains the main function that initializes the Qt
 * application, sets up the GUI, and starts the Go service for P2P.
 *
 * \note The peer ID is currently hardcoded. GUI uses a Peer dummy class instead
 * of the Go service. The real functionality is to be provided by the Go daemon.
 *
 * \see gui/app.h
 */

#include "gui/app.h"
#include "p2p/peer.h"
#include <QDebug> // For qWarning if peerId is empty
#include <clocale>

int main(int argc, char* argv[]) {
    // TODO: Replace with proper peer handling by the Go service.
    std::string peerId = "defaultPeerId";
    if (peerId.empty()) {
        qWarning() << "Peer ID is empty, using default. Pipe creation might "
                      "fail or collide.";
        peerId = "errorPeerId";
    }

    // Construct the Peer object with the chosen peer ID.
    P2P::Peer peer(peerId);
    // Launch the main Qt GUI event loop.
    gui::runGUI(&peer);
    return 0;
}