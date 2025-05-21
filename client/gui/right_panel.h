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
#pragma once

#include "gui/app.h"
#include "p2p/peer.h"
#include "roles/role_store.h"
#include "transport/control_stream_worker.h"
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
 * \param app Pointer to the main gui::App instance.
 * \param peer Pointer to the P2P::Peer object representing the local user.
 * \param window Pointer to the main QMainWindow.
 * \param worker Pointer to the ControlStreamWorker for sending gRPC messages.
 * \param roleStore Pointer to the RoleStore for permission checks.
 * \return QWidget* The constructed right panel widget.
 */
QWidget* createRightPanel(gui::App* app, P2P::Peer* peer, QMainWindow* window,
                          P2P::ControlStreamWorker* worker,
                          P2P::Roles::RoleStore* roleStore);

} // namespace gui
