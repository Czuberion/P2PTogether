#ifndef MENUS_H
#define MENUS_H

#include "p2p/peer.h"
#include <QMainWindow>
#include <QSplitter>
#include <QWidget>
#include <functional>

namespace gui {

// Creates the main window menus
void createMenus(QMainWindow* window, P2P::Peer* peer, QSplitter* mainSplitter,
                 QWidget* rightPanel, QWidget* leftPanel);

// Global callback for toggling the sidebar (used in video panel)
extern std::function<void()> SidebarToggleCallback;

} // namespace gui

#endif // MENUS_H
