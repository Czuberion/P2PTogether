/*!
 * \file
 * \brief The video panel embedding the MpvWidget for video playback and media
 * controls.
 *
 * The video panel is responsible for playback UI, role-gated controls, and
 * sidebar toggling, supporting synchronized viewing and usability.
 *
 * \see player/mpv_manager.h
 * \see player/mpvwidget.h
 * \see PRD F‑M2, F‑M4
 * \see SRS SR‑UI‑2, SR‑UI‑3
 */
#ifndef VIDEO_PANEL_H
#define VIDEO_PANEL_H

#include <QMainWindow> // Needed for fullscreen toggle
#include <QWidget>

// Forward declarations for dependencies
namespace player {
class MpvManager;
}

namespace gui {

/*!
 * \brief Creates the video panel for the GUI.
 *
 * Creates the main video panel widget, including the embedded MpvWidget and
 * media controls.
 *
 * \param mpvManager Pointer to the MpvManager instance for controlling mpv.
 * \param mainWindow Pointer to the main QMainWindow (for fullscreen toggling).
 * \return QWidget* The constructed video panel widget.
 */
QWidget* createVideoPanel(player::MpvManager* mpvManager,
                          QMainWindow* mainWindow);

} // namespace gui

#endif // VIDEO_PANEL_H