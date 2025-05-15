#include "gui/video_panel.h"
#include "client.pb.h"
#include "gui/menus.h" // for SidebarToggleCallback
#include "p2p/playback.pb.h"
#include "player/mpv_manager.h" // Include the MpvManager header
#include "player/mpvwidget.h"   // Include the MpvWidget header

#include <QDateTime>
#include <QDebug>
#include <QHBoxLayout>
#include <QLabel>
#include <QMainWindow>
#include <QPushButton>
#include <QVBoxLayout>
#include <QWidget>
#include <stdexcept>

namespace gui {

// Helper to get current state from MpvWidget
static void getCurrentPlaybackState(player::MpvWidget* mpvWidget, double& pos,
                                    bool& isPlaying, double& speed) {
    if (!mpvWidget)
        return;
    // Use getProperty, default to sensible values if property missing/error
    pos       = mpvWidget->getProperty("time-pos").toDouble();
    isPlaying = !mpvWidget->getProperty("pause")
                     .toBool(); // pause=true means not playing
    speed = mpvWidget->getProperty("speed").toDouble();
}

// Helper function to send SetPlaybackStateCmd
static void sendPlaybackStateCommand(P2P::ControlStreamWorker* worker,
                                     double targetPos, bool targetIsPlaying,
                                     double targetSpeed) {
    if (!worker) {
        qWarning()
            << "Cannot send SetPlaybackStateCmd: ControlStreamWorker is null";
        return;
    }
    client::ClientMsg clientMsg;
    client::p2p::SetPlaybackStateCmd* cmd =
        clientMsg.mutable_playback_state_cmd(); // Use correct field name
    cmd->set_target_time_pos(targetPos);
    cmd->set_target_is_playing(targetIsPlaying);
    cmd->set_target_speed(targetSpeed);
    cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch()); // Set HLC timestamp
    worker->send(clientMsg);
    qDebug() << "Sent SetPlaybackStateCmd: Pos" << targetPos << "Playing"
             << targetIsPlaying << "Speed" << targetSpeed;
}

QWidget* createVideoPanel(player::MpvManager* mpvManager,
                          QMainWindow* mainWindow,
                          P2P::ControlStreamWorker* worker) {
    // Container for video and controls
    QWidget* videoPanel = new QWidget();
    QVBoxLayout* layout = new QVBoxLayout(videoPanel);
    videoPanel->setLayout(layout);
    layout->setContentsMargins(0, 0, 0, 0); // no outer gaps
    layout->setSpacing(0);                  // no inner gaps

    // --- Video Widget ---
    // Replace the placeholder QWidget with MpvWidget
    player::MpvWidget* mpvWidget = nullptr;
    try {
        mpvWidget = new player::MpvWidget();
        mpvWidget->setMinimumSize(100, 100);
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

    // --- Load the HLS URL ---
    QObject::connect(mpvManager, &player::MpvManager::playlistReady,
                     [mpvWidget](const QString& url) {
                         qInfo() << "Playlist ready, loading HLS URL:" << url;
                         mpvWidget->command(QStringList() << "loadfile" << url);
                     });

    // Display a “waiting for stream…” overlay until ready:
    QLabel* waiting = new QLabel("Waiting for stream…", mpvWidget);
    waiting->setAlignment(Qt::AlignCenter);
    waiting->setStyleSheet("color: white; background: rgba(0,0,0,0.5);");
    waiting->setAttribute(Qt::WA_TransparentForMouseEvents);

    // put it in a zero-margin V-box on top of the video widget:
    auto* overlayLayout = new QVBoxLayout(mpvWidget);
    overlayLayout->setContentsMargins(0, 0, 0, 0);
    overlayLayout->addStretch();
    overlayLayout->addWidget(waiting, /*stretch=*/0, Qt::AlignCenter);
    overlayLayout->addStretch();
    waiting->show();
    QObject::connect(mpvManager, &player::MpvManager::playlistReady, waiting,
                     &QWidget::hide);

    // --- Media Control Bar ---
    QWidget* controlBar = new QWidget();
    controlBar->setMaximumHeight(60); // Give controls some space
    QHBoxLayout* controlLayout = new QHBoxLayout(controlBar);
    controlBar->setLayout(controlLayout);
    controlLayout->setContentsMargins(5, 5, 5, 5); // Add some margins

    // --- Control Buttons ---

    // Skip back (assuming previous item in a playlist - not implemented yet)
    QPushButton* skipBackBtn = new QPushButton("⏮");
    skipBackBtn->setFixedSize(40, 40);
    skipBackBtn->setStyleSheet("font-size: 20px; text-align: center;");
    skipBackBtn->setToolTip("Skip to previous (NYI)");
    controlLayout->addWidget(skipBackBtn);

    // Rewind (seek backwards)
    QPushButton* rewindBtn = new QPushButton("⏪");
    rewindBtn->setFixedSize(40, 40);
    rewindBtn->setStyleSheet("font-size: 20px; text-align: center;");
    rewindBtn->setToolTip("Rewind 5 seconds");
    QObject::connect(rewindBtn, &QPushButton::clicked, [mpvWidget, worker]() {
        if (!mpvWidget)
            return;
        double currentPos, currentSpeed;
        bool currentIsPlaying;
        getCurrentPlaybackState(mpvWidget, currentPos, currentIsPlaying,
                                currentSpeed);
        double targetPos = qMax(0.0, currentPos - 5.0);

        mpvWidget->command(QStringList() << "seek" << QString::number(targetPos)
                                         << "absolute");
        sendPlaybackStateCommand(worker, targetPos, currentIsPlaying,
                                 currentSpeed);
    });
    controlLayout->addWidget(rewindBtn);

    // Play/Pause Button
    QPushButton* playPauseBtn = new QPushButton("▶"); // Start with play icon
    playPauseBtn->setFixedSize(40, 40);
    playPauseBtn->setStyleSheet("font-size: 20px; text-align: center;");
    playPauseBtn->setObjectName("playPauseBtn");
    playPauseBtn->setToolTip("Play");
    QObject::connect(
        playPauseBtn, &QPushButton::clicked,
        [mpvWidget, worker, playPauseBtn]() {
            if (!mpvWidget)
                return;
            double currentPos, currentSpeed;
            bool currentIsPlaying;
            getCurrentPlaybackState(mpvWidget, currentPos, currentIsPlaying,
                                    currentSpeed);
            mpvWidget->setProperty("pause", currentIsPlaying);
            playPauseBtn->setText(!currentIsPlaying ? "⏸" : "▶");
            playPauseBtn->setToolTip(!currentIsPlaying ? "Pause" : "Play");

            sendPlaybackStateCommand(worker, currentPos, !currentIsPlaying,
                                     currentSpeed);
        });
    controlLayout->addWidget(playPauseBtn);

    // Fast Forward (seek forwards)
    QPushButton* fastForwardBtn = new QPushButton("⏩");
    fastForwardBtn->setFixedSize(40, 40);
    fastForwardBtn->setStyleSheet("font-size: 20px; text-align: center;");
    fastForwardBtn->setToolTip("Fast forward 5 seconds");
    QObject::connect(fastForwardBtn, &QPushButton::clicked,
                     [mpvWidget, worker]() { // Capture worker
                         if (!mpvWidget)
                             return;
                         double currentPos, currentSpeed;
                         bool currentPlaying;
                         getCurrentPlaybackState(mpvWidget, currentPos,
                                                 currentPlaying, currentSpeed);
                         double targetPos = currentPos + 5.0;

                         mpvWidget->command(QStringList()
                                            << "seek"
                                            << QString::number(targetPos)
                                            << "absolute"); // Apply locally
                         sendPlaybackStateCommand(worker, targetPos,
                                                  currentPlaying, currentSpeed);
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
    QObject::connect(
        stopButton, &QPushButton::clicked, [mpvWidget, worker, playPauseBtn]() {
            if (!mpvWidget)
                return;
            double _pos, currentSpeed;
            bool _playing;
            getCurrentPlaybackState(mpvWidget, _pos, _playing, currentSpeed);

            mpvWidget->setProperty("pause", true);
            mpvWidget->command(QStringList() << "seek" << "0" << "absolute");
            playPauseBtn->setText("▶");
            playPauseBtn->setToolTip("Play");
            sendPlaybackStateCommand(worker, 0.0, false, currentSpeed);
        });
    controlLayout->addWidget(stopButton);

    // Replay Button (seek to start)
    QPushButton* replayBtn = new QPushButton("🔄");
    replayBtn->setFixedSize(40, 40);
    replayBtn->setStyleSheet("font-size: 20px; text-align: center;");
    replayBtn->setToolTip("Replay (seek to beginning)");
    QObject::connect(replayBtn, &QPushButton::clicked, [mpvWidget, worker]() {
        if (!mpvWidget)
            return;
        double _pos, currentSpeed;
        bool currentPlaying;
        getCurrentPlaybackState(mpvWidget, _pos, currentPlaying, currentSpeed);

        mpvWidget->command(QStringList() << "seek" << "0" << "absolute");
        sendPlaybackStateCommand(worker, 0.0, currentPlaying, currentSpeed);
    });
    controlLayout->addWidget(replayBtn);

    controlLayout->addSpacing(30);
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