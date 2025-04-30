/*!
 * \file
 * \brief Declares the right panel (chat, queue, analytics) for the P2PTogether
 * GUI.
 *
 * The right panel implements chat, queue, and analytics dashboard, supporting
 * requirements for real-time interaction, queue management, and analytics.
 *
 * \see PRD F‑M3, F‑S2
 * \see SRS SR‑CQ‑1, SR‑AN‑1
 */
#ifndef RIGHT_PANEL_H
#define RIGHT_PANEL_H

#include "p2p/peer.h"
#include <QMainWindow>
#include <QWidget>

namespace gui {

/*!
 * \brief Callback function for refreshing the visibility of queue buttons.
 *
 * This function is set in the menus and called when the queue button
 * visibility needs to be updated based on the user's roles.
 */
extern std::function<void()> QueueButtonsRefreshCallback;

/*!
 * \brief Creates the right panel widget for the GUI.
 *
 * This function sets up the chat, queue, and analytics dashboard in the right
 * panel of the main window.
 *
 * \param peer Pointer to the P2P::Peer object representing the local user.
 * \param window Pointer to the main QMainWindow.
 * \return QWidget* The constructed right panel widget.
 */
QWidget* createRightPanel(P2P::Peer* peer, QMainWindow* window);

} // namespace gui

#endif // RIGHT_PANEL_H
