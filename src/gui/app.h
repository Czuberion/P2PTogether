#ifndef APP_H
#define APP_H

#include "p2p/peer.h"
#include <QApplication>
#include <QMainWindow>

namespace gui {

void runGUI(P2P::Peer* peer);

}

#endif // APP_H
