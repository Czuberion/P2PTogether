#include "gui/video_panel.h"
#include "gui/menus.h"          // for SidebarToggleCallback
#include "player/mpv_manager.h" // Include the MpvManager header
#include "player/mpvwidget.h"   // Include the MpvWidget header

#include <QDebug>
#include <QHBoxLayout>
#include <QLabel>
#include <QMainWindow>
#include <QPushButton>
#include <QVBoxLayout>
#include <QWidget>
#include <stdexcept> // For runtime_error

namespace gui {

// Update signature to accept MpvManager and QMainWindow
QWidget* createVideoPanel(player::MpvManager* mpvManager,
                          QMainWindow* mainWindow) {
    // Container for video and controls
    QWidget* videoPanel = new QWidget();
    QVBoxLayout* layout = new QVBoxLayout(videoPanel);
    videoPanel->setLayout(layout);

    // --- Video Widget ---
    // Replace the placeholder QWidget with MpvWidget
    player::MpvWidget* mpvWidget = nullptr;
    try {
        mpvWidget = new player::MpvWidget();
        mpvWidget->setMinimumSize(640, 360);
        // Set background for aesthetics before video loads/if error
        mpvWidget->setStyleSheet("background-color: #1a1a1a;");
    } catch (const std::runtime_error& e) {
        qCritical() << "Failed to create MpvWidget:" << e.what();
        // Return a simple error widget instead
        QLabel* errorLabel = new QLabel(
            "Error initializing video player.\nCheck console for details.");
        errorLabel->setAlignment(Qt::AlignCenter);
        errorLabel->setStyleSheet(
            "color: red; background-color: #1a1a1a; font-size: 18px;");
        layout->addWidget(errorLabel, 1); // Add error label
        return videoPanel; // Return the panel containing the error
    }

    // --- Load the Pipe ---
    QString pipePath = mpvManager->getPipePath();
    if (!pipePath.isEmpty()) {
        qInfo() << "Telling mpv to load file:" << pipePath;
        mpvWidget->command(QStringList() << "loadfile" << pipePath);
        // Optional: Set some initial properties
        // mpvWidget->setProperty("pause", true); // Start paused
        // mpvWidget->setProperty("loop-file",
        //                        "inf"); // Loop the pipe content if needed
    } else {
        qWarning() << "Pipe path is empty, cannot load into mpv.";
        // Optionally display a message on the video widget
        QLabel* noPipeLabel = new QLabel("No input pipe configured", mpvWidget);
        noPipeLabel->setAlignment(Qt::AlignCenter);
        noPipeLabel->setStyleSheet(
            "color: yellow; background-color: rgba(0,0,0,0.5);");
        QVBoxLayout* overlayLayout =
            new QVBoxLayout(mpvWidget); // Need layout in mpvWidget for label
        overlayLayout->addWidget(noPipeLabel, 0, Qt::AlignCenter);
    }

    // --- Media Control Bar ---
    QWidget* controlBar = new QWidget();
    controlBar->setMaximumHeight(60); // Give controls some space
    QHBoxLayout* controlLayout = new QHBoxLayout(controlBar);
    controlBar->setLayout(controlLayout);
    controlLayout->setContentsMargins(5, 0, 5, 5); // Add some margins

    // --- Control Buttons ---

    // Skip back (assuming previous item in a playlist - not implemented yet)
    QPushButton* skipBtn = new QPushButton("⏮");
    skipBtn->setFixedSize(40, 40);
    skipBtn->setStyleSheet("font-size: 20px; text-align: center;");
    skipBtn->setToolTip("Skip to previous (NYI)");
    controlLayout->addWidget(skipBtn);

    // Rewind (seek backwards)
    QPushButton* rewindBtn = new QPushButton("⏪");
    rewindBtn->setFixedSize(40, 40);
    rewindBtn->setStyleSheet("font-size: 20px; text-align: center;");
    rewindBtn->setToolTip("Rewind 5 seconds");
    QObject::connect(rewindBtn, &QPushButton::clicked, [mpvWidget]() {
        mpvWidget->command(QStringList() << "seek" << "-5");
    });
    controlLayout->addWidget(rewindBtn);

    // Play/Pause Button
    QPushButton* playPauseBtn = new QPushButton("▶"); // Start with play icon
    playPauseBtn->setFixedSize(40, 40);
    playPauseBtn->setStyleSheet("font-size: 20px; text-align: center;");
    playPauseBtn->setToolTip("Play");
    // Connect to mpv property changes for robust state tracking (optional but
    // good) QObject::connect(mpvWidget, &MpvWidget::propertyChanged.... ) //
    // Needs propertyChanged signal in MpvWidget Simple toggle based on click
    // for now:
    QObject::connect(playPauseBtn, &QPushButton::clicked,
                     [=]() { // Use = to capture mpvWidget by value (pointer)
                         bool isPaused =
                             mpvWidget->getProperty("pause").toBool();
                         mpvWidget->setProperty("pause", !isPaused);
                         // Update button based on the *new* state
                         if (!isPaused) { // it was playing, now it's paused
                             playPauseBtn->setText("▶");
                             playPauseBtn->setToolTip("Play");
                         } else { // it was paused, now it's playing
                             playPauseBtn->setText("⏸");
                             playPauseBtn->setToolTip("Pause");
                         }
                     });
    controlLayout->addWidget(playPauseBtn);

    // Fast Forward (seek forwards)
    QPushButton* fastForwardBtn = new QPushButton("⏩");
    fastForwardBtn->setFixedSize(40, 40);
    fastForwardBtn->setStyleSheet("font-size: 20px; text-align: center;");
    fastForwardBtn->setToolTip("Fast forward 5 seconds");
    QObject::connect(fastForwardBtn, &QPushButton::clicked, [mpvWidget]() {
        mpvWidget->command(QStringList() << "seek" << "5");
    });
    controlLayout->addWidget(fastForwardBtn);

    // Skip forward (assuming next item in a playlist - not implemented yet)
    QPushButton* skipForwardBtn = new QPushButton("⏭");
    skipForwardBtn->setFixedSize(40, 40);
    skipForwardBtn->setStyleSheet("font-size: 20px; text-align: center;");
    skipForwardBtn->setToolTip("Skip to next (NYI)");
    controlLayout->addWidget(skipForwardBtn);

    controlLayout->addSpacing(30);

    // Stop Button (pause and seek to start)
    QPushButton* stopButton = new QPushButton("⏹");
    stopButton->setFixedSize(40, 40);
    stopButton->setStyleSheet("font-size: 20px; text-align: center;");
    stopButton->setToolTip("Stop (pause and rewind)");
    QObject::connect(stopButton, &QPushButton::clicked, [=]() {
        mpvWidget->setProperty("pause", true);
        mpvWidget->command(QStringList() << "seek" << "0" << "absolute");
        playPauseBtn->setText("▶"); // Update play/pause button too
        playPauseBtn->setToolTip("Play");
    });
    controlLayout->addWidget(stopButton);

    // Replay Button (seek to start)
    QPushButton* replayBtn = new QPushButton("🔄");
    replayBtn->setFixedSize(40, 40);
    replayBtn->setStyleSheet("font-size: 20px; text-align: center;");
    replayBtn->setToolTip("Replay (seek to beginning)");
    QObject::connect(replayBtn, &QPushButton::clicked, [mpvWidget]() {
        mpvWidget->command(QStringList() << "seek" << "0" << "absolute");
    });
    controlLayout->addWidget(replayBtn);

    controlLayout->addStretch(); // Pushes following items to the right

    // Screenshot Button (using mpv's command)
    QPushButton* screenshotBtn = new QPushButton("📷");
    screenshotBtn->setFixedSize(40, 40);
    screenshotBtn->setStyleSheet("font-size: 20px; text-align: center;");
    screenshotBtn->setToolTip("Take screenshot (saves to working dir)");
    QObject::connect(screenshotBtn, &QPushButton::clicked, [mpvWidget]() {
        // Saves to working directory by default, filename includes timestamp
        // Check mpv manual for 'screenshot-directory' and 'screenshot-template'
        // options
        mpvWidget->command(QStringList() << "screenshot");
    });
    controlLayout->addWidget(screenshotBtn);

    // Fullscreen Button (toggle main window fullscreen)
    QPushButton* fullscreenBtn = new QPushButton("⛶");
    fullscreenBtn->setFixedSize(40, 40);
    fullscreenBtn->setStyleSheet("font-size: 20px; text-align: center;");
    fullscreenBtn->setToolTip("Enter fullscreen");
    QObject::connect(
        fullscreenBtn, &QPushButton::clicked, [=]() { // Capture mainWindow
            if (mainWindow->isFullScreen()) {
                mainWindow->showNormal();
                fullscreenBtn->setText("⛶");
                fullscreenBtn->setToolTip("Enter fullscreen");
            } else {
                mainWindow->showFullScreen();
                fullscreenBtn->setText("╬"); // Use a different icon for exit
                fullscreenBtn->setToolTip("Exit fullscreen");
            }
        });
    controlLayout->addWidget(fullscreenBtn);

    controlLayout->addSpacing(30);

    // Sidebar Toggle Button
    QPushButton* sidebarToggleBtn = new QPushButton("☰");
    sidebarToggleBtn->setFixedSize(40, 40);
    sidebarToggleBtn->setStyleSheet("font-size: 20px; text-align: center;");
    sidebarToggleBtn->setToolTip("Toggle sidebar");
    QObject::connect(sidebarToggleBtn, &QPushButton::clicked, []() {
        // Use the global callback from menus.h
        if (SidebarToggleCallback)
            SidebarToggleCallback();
    });
    controlLayout->addWidget(sidebarToggleBtn);

    // --- Add widgets to layout ---
    layout->addWidget(mpvWidget, 1);  // Video widget takes most space
    layout->addWidget(controlBar, 0); // Control bar at the bottom

    return videoPanel;
}

} // namespace gui