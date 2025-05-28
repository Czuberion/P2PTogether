/*!
 * \file
 * \brief Declares menu creation and sidebar toggle callback for the P2PTogether
 * GUI.
 *
 * Menus provide access to session management, settings, peer management, and
 * help, supporting the requirements for role-based control and usability.
 *
 * \see PRD F‑M2, F‑M4
 * \see SRS SR‑UI‑1, SR‑UI‑4
 */
#pragma once

#include "gui/app.h"
#include "p2p/peer.h"
#include "roles/role_store.h"
#include "transport/control_stream_worker.h"
#include <QMainWindow>
#include <QSplitter>
#include <QWidget>
#include <functional>

namespace gui {

/*!
 * \brief Creates the application menus for the main window.
 *
 * This function sets up the session, view, roles, and help menus, and connects
 * them to the appropriate actions and callbacks.
 *
 * \param window Pointer to the main QMainWindow.
 * \param peer Pointer to the P2P::Peer object representing the local user.
 * \param mainSplitter Pointer to the main QSplitter for layout management.
 * \param rightPanel Pointer to the right panel widget (chat, queue, analytics).
 * \param leftPanel Pointer to the left panel widget (video playback).
 */
void createMenus(QMainWindow* window, gui::App* app,
                 P2P::Roles::RoleStore* roleStore, QSplitter* mainSplitter,
                 QWidget* rightPanel, QWidget* leftPanel,
                 P2P::ControlStreamWorker* worker);

/*!
 * \brief Callback function for toggling the sidebar visibility.
 *
 * This function is set in the menus, the video panel, and called when the
 * sidebar toggle action is triggered.
 */
extern std::function<void()> SidebarToggleCallback;

} // namespace gui
