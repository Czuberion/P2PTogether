#include "gui/video_panel.h"
#include "client.pb.h"
#include "gui/menus.h" // for SidebarToggleCallback
#include "p2p/playback.pb.h"
#include "player/mpv_manager.h" // Include the MpvManager header
#include "player/mpvwidget.h"   // Include the MpvWidget header
#include "roles/permissions.h"

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
static void
sendPlaybackStateCommand(P2P::ControlStreamWorker* worker,
                         //  double localTargetPos,
                         //  double targetPos,
                         //  bool targetIsPlaying,
                         double localTargetPos, // mpv's local time-pos
                         bool targetIsPlaying, double targetSpeed,
                         //  double originSec) {
                         gui::App* appInstance) { // Pass App instance
    if (!worker) {
        qWarning()
            << "Cannot send SetPlaybackStateCmd: ControlStreamWorker is null";
        return;
    }
    client::ClientMsg clientMsg;
    client::p2p::SetPlaybackStateCmd* cmd =
        clientMsg.mutable_playback_state_cmd(); // Use correct field name

    double originSec = 0.0;
    if (appInstance) {
        originSec = appInstance->playlistOriginSec();
    }

    // Send global time
    cmd->set_target_time_pos(localTargetPos + originSec);
    cmd->set_target_is_playing(targetIsPlaying);
    cmd->set_target_speed(targetSpeed);
    // Use 0 if appInstance is null or seqId not available
    cmd->set_stream_sequence_id(
        appInstance ? appInstance->getCurrentStreamSequenceId() : 0);
    cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch()); // Set HLC timestamp
    worker->send(clientMsg);
    // qDebug() << "Sent SetPlaybackStateCmd: Pos" << targetPos << "Playing"
    //          << targetIsPlaying << "Speed" << targetSpeed << "GlobalPos"
    //          << (localTargetPos + originSec);
    qDebug() << "Sent SetPlaybackStateCmd: LocalPos" << localTargetPos
             << "Playing" << targetIsPlaying << "Speed" << targetSpeed
             << "OriginSec" << originSec << "GlobalPos"
             << (localTargetPos + originSec);
}

QWidget* createVideoPanel(player::MpvManager* mpvManager,
                          QMainWindow* mainWindow,
                          P2P::ControlStreamWorker* worker, gui::App* app) {
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

    // Skip back
    QPushButton* skipBackBtn = new QPushButton("⏮");
    skipBackBtn->setFixedSize(40, 40);
    skipBackBtn->setStyleSheet("font-size: 20px; text-align: center;");
    skipBackBtn->setToolTip("Skip to previous");
    skipBackBtn->setObjectName("skipBackBtn");
    controlLayout->addWidget(skipBackBtn);
    QObject::connect(skipBackBtn, &QPushButton::clicked, [worker, app]() {
        if (!worker || !app)
            return;
        client::ClientMsg msg;
        auto* cmd = msg.mutable_queue_cmd();
        cmd->set_type(client::p2p::QueueCmd_Type_SKIP_PREV); // Use SKIP_PREV
        cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());
        worker->send(msg);
    });

    // Rewind (seek backwards)
    QPushButton* rewindBtn = new QPushButton("⏪");
    rewindBtn->setFixedSize(40, 40);
    rewindBtn->setStyleSheet("font-size: 20px; text-align: center;");
    rewindBtn->setToolTip("Rewind 5 seconds");
    QObject::connect(
        rewindBtn, &QPushButton::clicked, [mpvWidget, worker, app]() {
            if (!mpvWidget)
                return;
            double currentPos, currentSpeed;
            bool currentIsPlaying;
            getCurrentPlaybackState(mpvWidget, currentPos, currentIsPlaying,
                                    currentSpeed);
            double localTargetPos = qMax(0.0, currentPos - 5.0);

            mpvWidget->command(QStringList()
                               << "seek" << QString::number(localTargetPos)
                               << "absolute");
            // sendPlaybackStateCommand(worker, targetPos, currentIsPlaying,
            //                          currentSpeed);
            sendPlaybackStateCommand(worker, localTargetPos, currentIsPlaying,
                                     currentSpeed, app);
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
        [mpvWidget, worker, playPauseBtn, app]() {
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
                                     currentSpeed, app);
        });
    controlLayout->addWidget(playPauseBtn);

    // Fast Forward (seek forwards)
    QPushButton* fastForwardBtn = new QPushButton("⏩");
    fastForwardBtn->setFixedSize(40, 40);
    fastForwardBtn->setStyleSheet("font-size: 20px; text-align: center;");
    fastForwardBtn->setToolTip("Fast forward 5 seconds");
    QObject::connect(
        fastForwardBtn, &QPushButton::clicked, [mpvWidget, worker, app]() {
            if (!mpvWidget)
                return;
            double currentPos, currentSpeed;
            bool currentPlaying;
            getCurrentPlaybackState(mpvWidget, currentPos, currentPlaying,
                                    currentSpeed);
            double localTargetPos = currentPos + 5.0;

            mpvWidget->command(QStringList()
                               << "seek" << QString::number(localTargetPos)
                               << "absolute"); // Apply locally
            sendPlaybackStateCommand(worker, localTargetPos, currentPlaying,
                                     currentSpeed, app);
        });
    controlLayout->addWidget(fastForwardBtn);

    // Skip forward
    QPushButton* skipForwardBtn = new QPushButton("⏭");
    skipForwardBtn->setFixedSize(40, 40);
    skipForwardBtn->setStyleSheet("font-size: 20px; text-align: center;");
    skipForwardBtn->setToolTip("Skip to next");
    skipForwardBtn->setObjectName("skipForwardBtn");
    controlLayout->addWidget(skipForwardBtn);
    QObject::connect(skipForwardBtn, &QPushButton::clicked, [worker, app]() {
        if (!worker || !app)
            return;
        client::ClientMsg msg;
        auto* cmd = msg.mutable_queue_cmd();
        cmd->set_type(client::p2p::QueueCmd_Type_SKIP_NEXT);
        cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());
        worker->send(msg);
    });

    controlLayout->addSpacing(30);

    // Stop Button (pause and seek to start)
    QPushButton* stopButton = new QPushButton("⏹");
    stopButton->setFixedSize(40, 40);
    stopButton->setStyleSheet("font-size: 20px; text-align: center;");
    stopButton->setToolTip("Stop (pause and rewind)");
    QObject::connect(
        stopButton, &QPushButton::clicked,
        [mpvWidget, worker, playPauseBtn, app]() {
            if (!mpvWidget)
                return;
            double _pos, currentSpeed;
            bool _playing;
            getCurrentPlaybackState(mpvWidget, _pos, _playing, currentSpeed);

            mpvWidget->setProperty("pause", true);
            mpvWidget->command(QStringList() << "seek" << "0" << "absolute");
            playPauseBtn->setText("▶");
            playPauseBtn->setToolTip("Play");
            sendPlaybackStateCommand(worker, 0.0, false, currentSpeed, app);
        });
    controlLayout->addWidget(stopButton);

    // Replay Button (seek to start)
    QPushButton* replayBtn = new QPushButton("🔄");
    replayBtn->setFixedSize(40, 40);
    replayBtn->setStyleSheet("font-size: 20px; text-align: center;");
    replayBtn->setToolTip("Replay (seek to beginning)");
    QObject::connect(
        replayBtn, &QPushButton::clicked, [mpvWidget, worker, app]() {
            if (!mpvWidget)
                return;
            double _pos, currentSpeed;
            bool currentPlaying;
            getCurrentPlaybackState(mpvWidget, _pos, currentPlaying,
                                    currentSpeed);

            mpvWidget->command(QStringList() << "seek" << "0" << "absolute");
            sendPlaybackStateCommand(worker, 0.0, currentPlaying, currentSpeed,
                                     app);
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

    // --- Button Enable/Disable Logic ---
    // This lambda will be connected to App's queueStateChanged and RoleStore
    // signals
    auto updateButtonStates = [app, skipBackBtn, skipForwardBtn, rewindBtn,
                               playPauseBtn, fastForwardBtn, stopButton,
                               replayBtn, screenshotBtn]() {
        if (!app)
            return;

        // Default to disabled if RoleStore is unavailable
        bool canQueue     = false;
        bool canPlayPause = false;
        bool canSeek      = false;

        P2P::Roles::RoleStore* roleStore =
            app->findChild<P2P::Roles::RoleStore*>();
        if (roleStore) {
            quint32 perms =
                roleStore->getPermissionsForPeer(roleStore->getLocalPeerId());
            canQueue = P2P::Roles::hasPermission(perms, P2P::Roles::PermQueue);
            canPlayPause =
                P2P::Roles::hasPermission(perms, P2P::Roles::PermPlayPause);
            canSeek = P2P::Roles::hasPermission(perms, P2P::Roles::PermSeek);
        }

        // Update queue-related buttons (skip)
        QList<client::p2p::QueueItem> items = app->getCurrentQueueItems();
        int currentIndex                    = app->getCurrentPlayingIndex();
        int queueSize                       = items.size();
        bool hasVideo                       = queueSize > 0;

        // Set visibility based on permissions
        skipBackBtn->setVisible(canQueue);
        skipForwardBtn->setVisible(canQueue);
        playPauseBtn->setVisible(canPlayPause);
        rewindBtn->setVisible(canSeek);
        fastForwardBtn->setVisible(canSeek);
        replayBtn->setVisible(canSeek);
        stopButton->setVisible(canPlayPause && canSeek);

        // Enable/disable buttons based on queue/video state and permissions
        skipBackBtn->setEnabled(canQueue && currentIndex > 0 && hasVideo);
        skipForwardBtn->setEnabled(canQueue && currentIndex >= 0 &&
                                   currentIndex < queueSize - 1 && hasVideo);
        playPauseBtn->setEnabled(canPlayPause && hasVideo);
        rewindBtn->setEnabled(canSeek && hasVideo);
        fastForwardBtn->setEnabled(canSeek && hasVideo);
        replayBtn->setEnabled(canSeek && hasVideo);
        stopButton->setEnabled(canPlayPause && canSeek && hasVideo);
        screenshotBtn->setEnabled(hasVideo);
    };

    // Connect to App's signal for queue/playback index changes
    QObject::connect(app, &gui::App::queueStateChanged, videoPanel,
                     updateButtonStates);
    // Connect to RoleStore signals for permission changes
    P2P::Roles::RoleStore* roleStore = app->findChild<P2P::Roles::RoleStore*>();
    if (roleStore) {
        QObject::connect(
            roleStore, &P2P::Roles::RoleStore::peerRolesChanged, videoPanel,
            [app, roleStore, updateButtonStates](const QString& peerId) {
                if (app && roleStore && peerId == roleStore->getLocalPeerId())
                    updateButtonStates();
            });
        QObject::connect(roleStore, &P2P::Roles::RoleStore::definitionsChanged,
                         videoPanel, updateButtonStates);
        QObject::connect(roleStore,
                         &P2P::Roles::RoleStore::allAssignmentsRefreshed,
                         videoPanel, updateButtonStates);
        QObject::connect(roleStore,
                         &P2P::Roles::RoleStore::localPeerIdConfirmed,
                         videoPanel, updateButtonStates);
    }
    updateButtonStates(); // Call once for initial state

    return videoPanel;
}

} // namespace gui