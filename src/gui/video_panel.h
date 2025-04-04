#ifndef VIDEO_PANEL_H
#define VIDEO_PANEL_H

#include <QMainWindow> // Need for fullscreen toggle
#include <QWidget>

// Forward declarations
namespace player {
class MpvManager;
}

namespace gui {

// Update signature to accept MpvManager and QMainWindow
QWidget* createVideoPanel(player::MpvManager* mpvManager,
                          QMainWindow* mainWindow);

} // namespace gui

#endif // VIDEO_PANEL_H