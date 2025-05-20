#include "gui/app.h"
#include "client.pb.h"
#include "gui/menus.h"
#include "gui/right_panel.h"
#include "gui/video_panel.h"
#include "p2p/playback.pb.h"
#include "player/mpv_manager.h"
#include "transport/grpc_client.h"

#include <QApplication>
#include <QDebug>
#include <QHBoxLayout>
#include <QMainWindow>
#include <QMetaObject>
#include <QPushButton>
#include <QSplitter>
#include <QThread>
#include <QWidget>
#include <clocale>
#include <stdexcept>

namespace gui {

App::App(quint16 grpcPort, QObject* parent) :
    // QObject(parent), m_grpcPort(grpcPort), m_lastAppliedPlaybackHlc(-1) {
    QObject(parent),
    m_grpcPort(grpcPort),
    m_lastAppliedPlaybackHlc(-1),
    m_playlistOriginSec(0.0) {
    // Initialize other members to nullptr or default values if necessary
    m_roleStore =
        new P2P::Roles::RoleStore(this); // Parent to App for auto-deletion
}

App::~App() {
    qInfo() << "App::~App() destructor called.";
    cleanup(); // Ensure cleanup is called
    // m_roleStore is managed by QObject parent-child, no need to delete
    // explicitly m_worker and m_mainWindow might need explicit deletion if not
    // parented or handled by Qt
}

void App::onServerMessage(const client::ServerMsg& msg) {
    qInfo() << "[control‑stream] App::onServerMessage received payload type"
            << msg.payload_case();

    switch (msg.payload_case()) {
    case client::ServerMsg::kRoleDefinitionsUpdate:
        m_roleStore->processRoleDefinitionsUpdate(
            msg.role_definitions_update());
        break;
    case client::ServerMsg::kPeerRoleAssignment:
        m_roleStore->processPeerRoleAssignment(msg.peer_role_assignment());
        break;
    case client::ServerMsg::kAllPeerRoleAssignments:
        m_roleStore->processAllPeerAssignments(msg.all_peer_role_assignments());
        break;
    case client::ServerMsg::kLocalPeerIdentity:
        m_roleStore->setLocalPeerId(
            QString::fromStdString(msg.local_peer_identity().peer_id()));
        qInfo() << "GUI: Received LocalPeerIdentity, my ID is now:"
                << m_roleStore->getLocalPeerId();
        break;
    case client::ServerMsg::kPlaybackStateCmd: {
        if (!m_mpvWidget) {
            qWarning() << "Received PlaybackStateCmd but m_mpvWidget is null!";
            break;
        }
        const auto& cmd = msg.playback_state_cmd();
        qInfo() << "Received PlaybackStateCmd HLC:" << cmd.hlc_ts()
                << " vs LastApplied:" << m_lastAppliedPlaybackHlc;

        if (cmd.hlc_ts() > m_lastAppliedPlaybackHlc) {
            qInfo() << "Applying received PlaybackStateCmd: Play"
                    << cmd.target_is_playing() << "Pos" << cmd.target_time_pos()
                    << "Speed" << cmd.target_speed();

            // Translate incoming global time_pos to local mpv time
            double localTargetPos = cmd.target_time_pos() - m_playlistOriginSec;
            // Clamp to 0 if origin_sec makes it negative (e.g. if origin_sec
            // just changed)
            if (localTargetPos < 0) {
                qInfo() << "Clamping localTargetPos from" << localTargetPos
                        << "to 0. Global:" << cmd.target_time_pos()
                        << "Origin:" << m_playlistOriginSec;
                localTargetPos = 0.0;
            }

            m_mpvWidget->setProperty("speed", cmd.target_speed());
            // mpv's 'seek X absolute' preserves play/pause state
            m_mpvWidget->command(QStringList()
                                 << "seek" << QString::number(localTargetPos)
                                 << "absolute");
            m_mpvWidget->setProperty("pause", !cmd.target_is_playing());

            if (m_playPauseBtn) {
                m_playPauseBtn->setText(cmd.target_is_playing() ? "⏸" : "▶");
                m_playPauseBtn->setToolTip(cmd.target_is_playing() ? "Pause"
                                                                   : "Play");
            }
            // if (m_seekSlider)
            // m_seekSlider->setValue(static_cast<int>(localTargetPos));

            // It's crucial that PlaybackStateCmd applies only if it's truly
            // newer than any PlaylistReset that might have also occurred. For
            // now, simple HLC comparison is fine.
            m_lastAppliedPlaybackHlc = cmd.hlc_ts();
        } else {
            qInfo() << "Ignoring stale PlaybackStateCmd HLC:" << cmd.hlc_ts();
        }
    } break;
    case client::ServerMsg::kPlaylistReset: {
        if (!m_mpvWidget) {
            qWarning() << "Received PlaylistReset but m_mpvWidget is null!";
            break;
        }
        const auto& resetCmd = msg.playlist_reset();
        qInfo() << "App::onServerMessage: PROCESSING PlaylistReset (Type 10):"
                << "New Sequence" << resetCmd.sequence() << "Start PTS"
                << resetCmd.start_pts() << "HLC" << resetCmd.hlc_ts()
                << "OriginSec" << resetCmd.origin_sec();

        m_playlistOriginSec = resetCmd.origin_sec(); // Store new origin

        // Force mpv to reload the playlist. This will make it pick up new
        // MEDIA-SEQUENCE and DISCONTINUITY. The MpvManager should still be
        // polling the same HLS URL.
        m_mpvWidget->command(QStringList()
                             << "loadfile" << m_mpvManager->getStreamURL()
                             << "replace");
        // Explicitly seek to the start_pts of the new playlist item.
        // This ensures that after a reload due to PlaylistReset, mpv's position
        // is correctly anchored to the beginning of the new content.
        // Using "exact" can help if precise seeking is needed without snapping.
        m_mpvWidget->command(QStringList()
                             << "seek" << QString::number(resetCmd.start_pts())
                             << "absolute" << "exact");

        // Treat PlaylistReset as an authoritative playback-affecting command.
        // This prevents older PlaybackStateCmds from being applied to the new
        // content.
        m_lastAppliedPlaybackHlc = resetCmd.hlc_ts();
    } break;
    case client::ServerMsg::kQueueUpdate: // ADD THIS CASE
        // The actual update of the QListWidget is handled by the
        // connection in right_panel.cpp.
        // App itself might not need to do anything, or just log.
        qInfo() << "App::onServerMessage: Received QueueUpdate (Type 1) - "
                   "primarily handled by right_panel.";
        break;
    // TODO: Handle kStreamStatus, kChatMsg
    default:
        qInfo() << "App::onServerMessage: Unhandled message type:"
                << msg.payload_case();
        break;
    }
}

void App::onNetThreadStarted() {
    qInfo() << "App::onNetThreadStarted - netThread (ID: "
            << QThread::currentThreadId() << ") has started for worker.";
}

void App::setupP2P() {
    P2P::GrpcClient grpcClient(
        QString("127.0.0.1:%1").arg(m_grpcPort).toStdString());

    QThread* netThread = new QThread(this);
    m_worker           = new P2P::ControlStreamWorker(
        std::make_unique<P2P::GrpcClient>(std::move(grpcClient)), nullptr);

    qInfo() << "App::setupP2P - Worker created, moving to new thread. Main "
               "thread ID:"
            << QThread::currentThreadId();

    m_worker->moveToThread(netThread);

    qInfo() << "App::setupP2P - Worker affinity set to netThread (ID: "
            << netThread << ").";

    // Connect App's slot to netThread's started signal
    connect(netThread, &QThread::started, this, &App::onNetThreadStarted);
    // Connect netThread's started signal to worker's start slot
    connect(netThread, &QThread::started, m_worker,
            &P2P::ControlStreamWorker::start);

    // Other connections for worker finished, thread finished, etc.
    connect(m_worker, &P2P::ControlStreamWorker::serverMsg, this,
            &App::onServerMessage);

    connect(m_worker, &P2P::ControlStreamWorker::finished, this,
            [](const QString& reason) {
                // This lambda runs in the main thread due to Qt::AutoConnection
                // (default) or explicit Qt::QueuedConnection.
                qInfo() << "App LAMBDA: ControlStreamWorker finished. Reason: "
                        << reason;
                // Any further complex UI updates or logic here should be
                // careful, as the application might be in the process of
                // shutting down.
            });

    connect(m_worker, &P2P::ControlStreamWorker::finished, netThread,
            &QThread::quit, Qt::QueuedConnection);
    qInfo()
        << "App::setupP2P - Connected m_worker::finished to netThread::quit.";

    connect(netThread, &QThread::finished, m_worker, &QObject::deleteLater,
            Qt::QueuedConnection); // worker first
    connect(netThread, &QThread::finished, netThread, &QObject::deleteLater);

    qInfo() << "App::setupP2P - Starting netThread (ID: " << netThread
            << ")...";

    netThread->start();

    qInfo() << "App::setupP2P - netThread->start() called. isRunning: "
            << netThread->isRunning();
}

void App::setupUI() {
    // --- Main Window Setup ---
    m_mainWindow = new QMainWindow(); // No parent, top-level window
    m_mainWindow->setWindowTitle("P2PTogether");
    m_mainWindow->resize(1200, 700);

    quint32 hlsPort = 0;
    { // Scope for GrpcClient to get HLS port
        P2P::GrpcClient tempGrpcClient(
            QString("127.0.0.1:%1").arg(m_grpcPort).toStdString());
        try {
            client::ServiceInfo serviceInfo = tempGrpcClient.getServiceInfo();
            hlsPort                         = serviceInfo.hls_port();
            qInfo() << "Successfully fetched HLS port from peer service:"
                    << hlsPort;
        } catch (const std::runtime_error& e) {
            qCritical() << "Failed to get service info from peer_service:"
                        << e.what();
            // Handle error appropriately, maybe exit or show message
            return;
        }
    }
    if (hlsPort == 0) {
        qCritical() << "Received invalid HLS port (0) from peer service.";
        return;
    }

    QString hlsUrl = QString("http://127.0.0.1:%1/stream.m3u8").arg(hlsPort);
    player::MpvManager* mpvManager =
        new player::MpvManager(hlsUrl, this); // Parent to App

    // --- Dummy Peer ---
    P2P::Peer peer("dummyAppPeer"); // Still needed if
                                    // createMenus/createRightPanel expect it

    // Create central widget and main layout
    QWidget* centralWidget = new QWidget(m_mainWindow);
    m_mainWindow->setCentralWidget(centralWidget);
    QHBoxLayout* mainLayout = new QHBoxLayout(centralWidget);
    centralWidget->setLayout(mainLayout);
    mainLayout->setContentsMargins(0, 0, 0, 0);
    mainLayout->setSpacing(0);

    QSplitter* mainSplitter = new QSplitter(Qt::Horizontal, centralWidget);
    mainLayout->addWidget(mainSplitter);

    QWidget* leftPanel =
        createVideoPanel(mpvManager, m_mainWindow, m_worker, this);
    if (!leftPanel) {
        qCritical() << "Failed to create video panel!";
        return;
    }
    mainSplitter->addWidget(leftPanel);
    m_mpvWidget =
        leftPanel->findChild<player::MpvWidget*>(); // Find after creation
    if (!m_mpvWidget)
        qWarning() << "App::setupUI: MpvWidget not found in leftPanel!";
    if (mpvManager) {
        m_mpvManager = mpvManager;
    }
    m_playPauseBtn = leftPanel->findChild<QPushButton*>("playPauseBtn");
    if (!m_playPauseBtn)
        qWarning() << "App::setupUI: playPauseBtn not found in leftPanel!";

    QWidget* rightPanel =
        createRightPanel(&peer, m_mainWindow, m_worker, m_roleStore);
    mainSplitter->addWidget(rightPanel);
    rightPanel->setMinimumWidth(300);

    QList<int> sizes;
    sizes << int(m_mainWindow->width() * 0.65)
          << int(m_mainWindow->width() * 0.35);
    mainSplitter->setSizes(sizes);
    mainSplitter->setCollapsible(0, false);

    createMenus(m_mainWindow, &peer, m_roleStore, mainSplitter, rightPanel,
                leftPanel, m_worker);
    m_mainWindow->show();
}

void App::cleanup() {
    qInfo() << "App::cleanup() entered.";
    if (m_isCleaningUp) {
        qInfo() << "App::cleanup() - already in progress, returning.";
        return;
    }
    m_isCleaningUp = true;

    if (m_worker) {
        QThread* netThread = m_worker->thread();
        if (netThread && netThread != QCoreApplication::instance()->thread()) {
            bool workerWasLikelyRunning = !m_worker->isQuitFlagSet();

            if (netThread->isRunning() || workerWasLikelyRunning) {
                qInfo() << "App::cleanup() - Requesting worker stop...";
                m_worker->stop(); // Tell the worker's run() loop to finish.

                qInfo() << "App::cleanup() - Worker stop requested. Processing "
                           "events to allow thread quit signal...";

                QEventLoop loop;
                QTimer::singleShot(
                    200, &loop,
                    &QEventLoop::quit); // Process events for max 200ms
                loop.exec();
                qInfo() << "App::cleanup() - Event processing finished. "
                           "netThread isRunning:"
                        << netThread->isRunning();
            } else {
                qInfo() << "App::cleanup() - Network thread was not running or "
                           "worker not in a separate thread.";
                if (m_worker) {
                    m_worker->deleteLater(); // Still use deleteLater for safety
                    m_worker = nullptr;
                }
            }
        } else {
            qInfo() << "App::cleanup() - Worker has no valid separate thread "
                       "or is in main thread.";
            if (m_worker) { // If worker exists but not in a separate manageable
                            // thread
                m_worker->deleteLater(); // Worker in main thread
                m_worker = nullptr;
            }
        }
    }

    if (m_mainWindow && !m_mainWindow->parent()) {
        qInfo() << "App::cleanup() - Main window exists and is top-level. Will "
                   "be handled by QApplication.";
    }
    m_mainWindow = nullptr;
    qInfo() << "App::cleanup() finished executing its own logic.";
}

int App::exec() {
    // QApplication instance is now created in main.cpp and passed or App
    // creates it. For this structure, main.cpp creates QApplication. App::exec
    // uses it.
    Q_ASSERT(QApplication::instance() != nullptr);

    std::setlocale(LC_NUMERIC, "C");
    QThread::msleep(500); // Allow peer_service to start

    setupP2P();
    setupUI();

    connect(QApplication::instance(), &QApplication::aboutToQuit, this,
            &App::cleanup);

    return QApplication::exec();
}

} // namespace gui