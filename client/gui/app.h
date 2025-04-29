/*!
 * \file
 * \brief Declares the entry point for launching the Qt GUI for P2PTogether.
 *
 * The GUI is the user-facing component of P2PTogether, a fully P2P desktop
 * application for synchronized video streaming and interaction. The GUI
 * interacts with the Peer Service (Go) via gRPC and provides session
 * management, playback, chat, queue, and analytics features.
 *
 * \see gui/menus.h
 * \see gui/right_panel.h
 * \see gui/video_panel.h
 * \see player/mpv_manager.h
 */
#ifndef APP_H
#define APP_H

#include "p2p/peer.h"
#include <QApplication>
#include <QMainWindow>

namespace gui {

/*!
 * \brief Launches the main Qt GUI for P2PTogether.
 *
 * Sets up the main window, video and side panels, and application menus.
 *
 * \param peer Pointer to the P2P::Peer dummy object representing the local
 * user.
 * \return The exit code of the QApplication event loop.
 */
int runGUI(P2P::Peer* peer);

} // namespace gui

#endif // APP_H
