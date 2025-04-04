#ifndef RIGHT_PANEL_H
#define RIGHT_PANEL_H

#include "p2p/peer.h"
#include <QMainWindow>
#include <QWidget>

namespace gui {

QWidget* createRightPanel(P2P::Peer* peer, QMainWindow* window);

}

#endif // RIGHT_PANEL_H
