#include "gui/app.h"
#include "client.pb.h"
#include "gui/menus.h"
#include "gui/right_panel.h"
#include "gui/video_panel.h"
#include "p2p/playback.pb.h"
#include "p2p/queue.pb.h"
#include "p2p/role.pb.h"
#include "p2p/session.pb.h"
#include "player/mpv_manager.h"
#include "transport/grpc_client.h"

#include <QApplication>
#include <QClipboard>
#include <QDebug>
#include <QHBoxLayout>
#include <QLabel>
#include <QMainWindow>
#include <QMessageBox>
#include <QMetaObject>
#include <QPushButton>
#include <QSplitter>
#include <QThread>
#include <QTimer>
#include <QWidget>
#include <clocale>
#include <stdexcept>

// Helper function (could be moved to a utility or be a static member of a
// dialog class)
static void showSessionInfoDialog(const QString& title,
                                  const QString& messageBody,
                                  const QString& sessionId,
                                  const QString& sessionName,
                                  const QString& inviteCode, QWidget* parent) {
    QDialog infoDialog(parent);
    infoDialog.setWindowTitle(title);
    QVBoxLayout* layout = new QVBoxLayout(&infoDialog);

    layout->addWidget(new QLabel(messageBody, &infoDialog));
    if (!sessionId.isEmpty()) {
        layout->addWidget(
            new QLabel(QString("Session ID: %1").arg(sessionId), &infoDialog));
    }
    if (!sessionName.isEmpty()) {
        layout->addWidget(new QLabel(
            QString("Session Name: %1").arg(sessionName), &infoDialog));
    }

    if (!inviteCode.isEmpty()) {
        QHBoxLayout* inviteLayout = new QHBoxLayout();
        inviteLayout->addWidget(new QLabel(
            QString("Invite Code: %1").arg(inviteCode), &infoDialog));
        QPushButton* copyButton = new QPushButton("⧉");
        copyButton->setToolTip("Copy Invite Code to Clipboard");
        copyButton->setFixedSize(copyButton->fontMetrics().height() * 2,
                                 copyButton->fontMetrics().height() * 2);
        QObject::connect(copyButton, &QPushButton::clicked, [inviteCode]() {
            QApplication::clipboard()->setText(inviteCode);
        });
        inviteLayout->addWidget(copyButton);
        inviteLayout->addStretch();
        layout->addLayout(inviteLayout);
    }

    QDialogButtonBox* buttonBox =
        new QDialogButtonBox(QDialogButtonBox::Ok, &infoDialog);
    QObject::connect(buttonBox, &QDialogButtonBox::accepted, &infoDialog,
                     &QDialog::accept);
    layout->addWidget(buttonBox);

    infoDialog.exec();
}

namespace gui {

App::App(quint16 grpcPort, QObject* parent) :
    // QObject(parent), m_grpcPort(grpcPort), m_lastAppliedPlaybackHlc(-1) {
    QObject(parent),
    m_grpcPort(grpcPort),
    m_localUsername(""),
    m_lastAppliedPlaybackHlc(-1),
    m_playlistOriginSec(0.0),
    m_activePlaylistSequenceId(0),
    m_playbackCompletedCurrentStream(false) {
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
    case client::ServerMsg::kRoleDefinitionsUpdate: {
        m_roleStore->processRoleDefinitionsUpdate(
            msg.role_definitions_update());
        break;
    }
    case client::ServerMsg::kPeerRoleAssignment: {
        m_roleStore->processPeerRoleAssignment(msg.peer_role_assignment());
        break;
    }
    case client::ServerMsg::kAllPeerRoleAssignments: {
        m_roleStore->processAllPeerAssignments(msg.all_peer_role_assignments());
        break;
    }
    case client::ServerMsg::kLocalPeerIdentity: {
        QString receivedPeerId =
            QString::fromStdString(msg.local_peer_identity().peer_id());

        // The first LocalPeerIdentity message received on stream connection
        // identifies the local peer. Subsequent ones are for other peers
        // joining the session. Only set the local ID if it's not already set.
        if (m_roleStore->getLocalPeerId().isEmpty()) {
            m_roleStore->setLocalPeerId(receivedPeerId);
        }

        qInfo() << "GUI: Received LocalPeerIdentity for peer:"
                << receivedPeerId;

        if (msg.local_peer_identity().has_username()) {
            QString username =
                QString::fromStdString(msg.local_peer_identity().username());
            m_roleStore->storePeerUsername(receivedPeerId, username);
            if (m_roleStore->getLocalPeerId() ==
                receivedPeerId) { // Check if it's for our local peer
                m_localUsername =
                    username; // Update App's direct m_localUsername too
                qInfo() << "GUI: My username (local peer) confirmed/updated to:"
                        << m_localUsername;
            }
        }
    } break;
    case client::ServerMsg::kPlaybackStateCmd: {
        if (!m_mpvWidget || !m_mpvManager) {
            qWarning() << "Received PlaybackStateCmd but m_mpvWidget is null!";
            break;
        }
        const auto& cmd = msg.playback_state_cmd();
        qInfo() << "Received PlaybackStateCmd for stream_sequence_id:"
                << cmd.stream_sequence_id() << "HLC:" << cmd.hlc_ts()
                << " vs LastApplied:" << m_lastAppliedPlaybackHlc;

        // CRITICAL: Check if the command is for the currently active stream
        // sequence. This prevents applying a playback command for a previous
        // item after a PlaylistReset for a new item has already occurred.
        if (cmd.stream_sequence_id() != m_activePlaylistSequenceId) {
            qInfo()
                << "Ignoring PlaybackStateCmd because its stream_sequence_id ("
                << cmd.stream_sequence_id()
                << ") does not match current active stream_sequence_id ("
                << m_activePlaylistSequenceId << "). HLC was " << cmd.hlc_ts();
            break;
        }

        if (cmd.hlc_ts() > m_lastAppliedPlaybackHlc) {
            qInfo() << "Applying PlaybackStateCmd for active stream "
                    << m_activePlaylistSequenceId << ": Play"
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
            qInfo() << "Ignoring stale PlaybackStateCmd for stream "
                    << cmd.stream_sequence_id() << "due to HLC:" << cmd.hlc_ts()
                    << " <= " << m_lastAppliedPlaybackHlc;
        }
    } break;
    case client::ServerMsg::kPlaylistReset: {
        if (!m_mpvWidget || !m_mpvManager) {
            qWarning() << "Received PlaylistReset but m_mpvWidget is null!";
            break;
        }
        const auto& resetCmd = msg.playlist_reset();
        qInfo() << "App::onServerMessage: PROCESSING PlaylistReset (Type 10):"
                << "New Sequence" << resetCmd.sequence() << "Start PTS"
                << resetCmd.start_pts() << "HLC" << resetCmd.hlc_ts()
                << "OriginSec" << resetCmd.origin_sec();

        m_playlistOriginSec = resetCmd.origin_sec(); // Store new origin
        m_activePlaylistSequenceId =
            resetCmd.sequence(); // PlaylistReset.sequence IS the stream/queue
                                 // item ID
        m_lastAppliedPlaybackHlc =
            resetCmd.hlc_ts(); // Update HLC for this authoritative event
        qInfo()
            << "App::onServerMessage: After PlaylistReset: m_playlistOriginSec="
            << m_playlistOriginSec
            << ", m_activePlaylistSequenceId=" << m_activePlaylistSequenceId
            << ", m_lastAppliedPlaybackHlc=" << m_lastAppliedPlaybackHlc;

        // Disconnect any previous one-shot seek connection to avoid multiple
        // triggers
        m_playbackCompletedCurrentStream =
            false; // Reset completion flag for the new stream
        if (m_seekOnLoadConnection) {
            qInfo() << "App::onServerMessage: Disconnecting previous "
                       "m_seekOnLoadConnection.";
            disconnect(m_seekOnLoadConnection);
        }

        // The video_panel is already connected to MpvManager::playlistReady to
        // do the 'loadfile'. We connect to the MpvWidget's fileLoaded signal,
        // which is emitted AFTER mpv confirms the file is actually loaded,
        // making it safe to seek.
        m_seekOnLoadConnection = connect(
            m_mpvWidget, &player::MpvWidget::fileLoaded, this,
            [this, startPts = resetCmd.start_pts()]() {
                qInfo() << "App::onServerMessage (Deferred Seek via "
                           "MpvWidget::fileLoaded): "
                           "mpv 'seek absolute exact' for pos:"
                        << startPts;
                if (m_mpvWidget) {
                    m_mpvWidget->command(QStringList()
                                         << "seek" << QString::number(startPts)
                                         << "absolute" << "exact");
                }
                // QMetaObject::Connection is automatically disconnected by
                // Qt::SingleShotConnection after firing but we clear our handle
                // to it.
                m_seekOnLoadConnection = {};
            },
            Qt::SingleShotConnection);

        // Re-trigger MpvManager's probing for the (potentially updated content
        // of the) same URL.
        m_mpvManager->setStreamURL(m_mpvManager->getStreamURL());
        qInfo() << "App::onServerMessage: MpvManager re-triggered for URL:"
                << m_mpvManager->getStreamURL();

        // Update current playing index based on PlaylistReset
        // This logic assumes PlaylistReset.sequence corresponds to
        // QueueItem.first_seq when that item starts playing.
        // Server is now expected to ensure first_seq is unique for items that
        // start.
        int oldPlayingIndex   = m_currentPlayingIndex;
        m_currentPlayingIndex = -1;

        // The server sets QueueItem.first_seq when an item *starts* playing.
        // This first_seq should match the PlaylistReset.sequence.
        // A first_seq of 0 is valid for the very first stream of a session.
        for (int i = 0; i < m_currentQueueItems.size(); ++i) {
            const auto& item = m_currentQueueItems.at(i);
            // Check if first_seq is set (it will be if the item has ever been
            // the head item that the server tried to start).
            // And if it matches the incoming reset command's sequence.
            // A freshly added item might have its default first_seq (0) until
            // the server actually processes it to play.
            // We rely on the PlaylistReset's sequence being the authoritative
            // one for the item *currently starting*.
            if (item.first_seq() ==
                m_activePlaylistSequenceId) { // Match against the new active
                                              // stream sequence
                m_currentPlayingIndex = i;
                break;
            }
        }
        if (m_currentPlayingIndex == -1 && m_currentQueueItems.size() == 1 &&
            m_activePlaylistSequenceId == 0) {
            // Special case: if only one item and sequence is 0, assume it's
            // that one.
            m_currentPlayingIndex = 0;
        }

        if (m_currentPlayingIndex != oldPlayingIndex) {
            emit queueStateChanged();
        }
    } break;
    case client::ServerMsg::kQueueUpdate: {
        m_currentQueueItems.clear();
        for (const auto& item : msg.queue_update().items()) {
            m_currentQueueItems.append(item);
        }
        // Note: currentPlayingIndex might become stale if items are
        // removed/reordered until the next PlaylistReset. For skip buttons,
        // this is usually fine.
        qInfo()
            << "App::onServerMessage: Received QueueUpdate (Type 1) - "
               "primarily handled by right_panel. Updating App's queue cache.";
        emit queueStateChanged(); // Emit signal as queue items list changed
        break;
    }
    case client::ServerMsg::kCreateSessionResponse: {
        const auto& resp = msg.create_session_response();
        if (resp.has_error_message()) {
            clearActiveSessionDetails();
            emit sessionCreationFailed(
                QString::fromStdString(resp.error_message()));
            emit sessionStateChanged(false);
        } else {
            setActiveSessionDetails(QString::fromStdString(resp.session_id()),
                                    QString::fromStdString(resp.invite_code()));
            emit sessionCreatedSuccessfully(
                QString::fromStdString(resp.session_id()),
                QString::fromStdString(resp.invite_code()));
            emit sessionStateChanged(true);
        }
    } break;
    case client::ServerMsg::kJoinSessionResponse: {
        const auto& resp = msg.join_session_response();
        if (resp.has_error_message()) {
            QMessageBox::critical(m_mainWindow, "Join Session Failed",
                                  QString::fromStdString(resp.error_message()));
            clearActiveSessionDetails();
            emit sessionStateChanged(false);
        } else {
            qInfo() << "GUI: Session joined successfully. ID:"
                    << QString::fromStdString(resp.session_id())
                    << "Snapshot size:" << resp.session_snapshot().size();

            setActiveSessionDetails(QString::fromStdString(resp.session_id()),
                                    QString::fromStdString(resp.session_id()));
            QString sessionNameFromJoinResp;
            if (resp.has_session_name()) {
                sessionNameFromJoinResp =
                    QString::fromStdString(resp.session_name());
            }

            if (!resp.session_snapshot().empty()) {
                client::p2p::SessionStateData session_state_data;
                if (session_state_data.ParseFromString(
                        resp.session_snapshot())) {
                    qInfo() << "GUI: Successfully parsed SessionStateData "
                               "snapshot.";

                    if (session_state_data.all_peer_identities_size() > 0) {
                        for (const auto& identity :
                             session_state_data.all_peer_identities()) {
                            m_roleStore->storePeerUsername(
                                QString::fromStdString(identity.peer_id()),
                                QString::fromStdString(identity.username()));
                            // If this identity is for the local peer, update
                            // m_localUsername
                            if (m_roleStore->getLocalPeerId() ==
                                QString::fromStdString(identity.peer_id())) {
                                if (identity.has_username()) {
                                    m_localUsername = QString::fromStdString(
                                        identity.username());
                                    qInfo() << "GUI: My username set to:"
                                            << m_localUsername
                                            << "from join snapshot.";
                                }
                            }
                        }
                        qInfo() << "GUI: Processed all_peer_identities from "
                                   "snapshot.";
                    }

                    if (session_state_data.has_all_role_assignments()) {
                        m_roleStore->processAllPeerAssignments(
                            session_state_data.all_role_assignments());
                        qInfo() << "GUI: Processed all_role_assignments from "
                                   "snapshot.";
                    } else {
                        qInfo() << "GUI: Snapshot does not contain "
                                   "all_role_assignments.";
                    }

                    if (session_state_data.has_current_queue_state()) {
                        const client::p2p::QueueUpdate& queue_update =
                            session_state_data.current_queue_state();
                        m_currentQueueItems.clear();
                        for (const auto& item_proto : queue_update.items()) {
                            m_currentQueueItems.append(item_proto);
                        }
                        qInfo() << "GUI: Processed current_queue_state from "
                                   "snapshot. Items:"
                                << m_currentQueueItems.size();
                        emit queueStateChanged();
                    } else {
                        qInfo() << "GUI: Snapshot does not contain "
                                   "current_queue_state.";
                        // Ensure queue is cleared if snapshot implies it should
                        // be empty
                        if (m_currentQueueItems.size() > 0) {
                            m_currentQueueItems.clear();
                            emit queueStateChanged();
                        }
                    }

                    // Process roles *after* queue, as role processing might
                    // trigger UI updates that depend on queue state (though
                    // less likely here).
                    if (session_state_data.has_all_role_assignments()) {
                        m_roleStore->processAllPeerAssignments(
                            session_state_data.all_role_assignments());
                        qInfo() << "GUI: Processed all_role_assignments from "
                                   "snapshot.";
                    }

                    // TODO: Process session_state_data.current_playback_state()
                } else {
                    qWarning() << "GUI: Failed to parse SessionStateData "
                                  "snapshot from JoinSessionResponse.";
                }
            } else {
                qInfo() << "GUI: JoinSessionResponse received without a "
                           "session snapshot. Clearing local queue if any.";
                // If no snapshot, ensure local state (like queue) is consistent
                // (e.g., empty)
                if (m_currentQueueItems.size() > 0) {
                    m_currentQueueItems.clear();
                    emit queueStateChanged();
                }
            }

            showSessionInfoDialog(
                "Session Joined", "Successfully joined session!",
                QString::fromStdString(resp.session_id()),
                sessionNameFromJoinResp,
                "", // No invite code to display/copy for "joined" dialog
                m_mainWindow);

            emit sessionStateChanged(true);
        }
    } break;
    case client::ServerMsg::kChatMsg: {
        const auto& chat_msg_proto = msg.chat_msg();
        QString sender_id    = QString::fromStdString(chat_msg_proto.sender());
        QString message_text = QString::fromStdString(chat_msg_proto.text());
        QString local_id_from_store;

        if (m_roleStore)
            local_id_from_store = m_roleStore->getLocalPeerId();
        bool is_self = (!local_id_from_store.isEmpty() &&
                        sender_id == local_id_from_store);

        QString sender_name_to_display;
        if (is_self) {
            sender_name_to_display =
                m_localUsername.isEmpty() ? sender_id : m_localUsername;
        } else {
            sender_name_to_display =
                m_roleStore ? m_roleStore->getPeerUsername(sender_id) : "";
            if (sender_name_to_display.isEmpty())
                sender_name_to_display = sender_id;
        }
        emit chatMessageReceived(sender_name_to_display, message_text, is_self);
    } break;
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
        // Connect MpvWidget's actualFileEnded signal
        if (m_mpvWidget) {
            connect(m_mpvWidget, &player::MpvWidget::actualFileEnded, this,
                    &App::onLocalPlayerFileEnded);
            connect(m_mpvWidget, &player::MpvWidget::positionChanged, this,
                    &App::onMpvPositionChanged);
        } else {
            qWarning() << "App::setupUI: MpvWidget is null, cannot connect "
                          "actualFileEnded signal.";
        }
        m_mpvManager = mpvManager;
    }
    m_playPauseBtn = leftPanel->findChild<QPushButton*>("playPauseBtn");
    if (!m_playPauseBtn)
        qWarning() << "App::setupUI: playPauseBtn not found in leftPanel!";

    QWidget* rightPanel =
        createRightPanel(this, m_mainWindow, m_worker, m_roleStore);
    mainSplitter->addWidget(rightPanel);
    rightPanel->setMinimumWidth(300);

    QTabWidget* rightPanelTabs =
        rightPanel->findChild<QTabWidget*>("rightPanelTabs");

    QList<int> sizes;
    sizes << int(m_mainWindow->width() * 0.65)
          << int(m_mainWindow->width() * 0.35);
    mainSplitter->setSizes(sizes);
    mainSplitter->setCollapsible(0, false);

    createMenus(m_mainWindow, this, m_roleStore, mainSplitter, rightPanel,
                rightPanelTabs, leftPanel, m_worker);
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

QList<client::p2p::QueueItem> App::getCurrentQueueItems() const {
    return m_currentQueueItems;
}

QString App::getCurrentSessionId() const {
    return m_currentSessionId;
}

QString App::getCurrentInviteCode() const {
    return m_currentInviteCode;
}

void App::setActiveSessionDetails(const QString& sessionId,
                                  const QString& inviteCode) {
    m_currentSessionId  = sessionId;
    m_currentInviteCode = inviteCode;
}

void App::clearActiveSessionDetails() {
    m_currentSessionId.clear();
    m_currentInviteCode.clear();
}

int App::getCurrentPlayingIndex() const {
    // Re-validate index against current queue size, just in case
    if (m_currentPlayingIndex >= 0 &&
        m_currentPlayingIndex < m_currentQueueItems.size()) {
        return m_currentPlayingIndex;
    }
    return -1;
}

uint32_t App::getCurrentStreamSequenceId() const {
    return m_activePlaylistSequenceId;
}

void App::onMpvPositionChanged(double newPosition) {
    if (m_playbackCompletedCurrentStream || !m_mpvWidget ||
        m_currentPlayingIndex < 0 ||
        m_currentPlayingIndex >= m_currentQueueItems.size()) {
        return;
    }

    const auto& currentItem = m_currentQueueItems.at(m_currentPlayingIndex);
    if (currentItem.first_seq() != m_activePlaylistSequenceId) {
        // Not playing the item we think we are, or item data not yet synced
        // for current active stream
        return;
    }

    // Get relevant properties from MpvWidget
    double mpvDuration = m_mpvWidget->getProperty("duration")
                             .toDouble(); // Total duration of the current
                                          // playlist content as seen by mpv
    bool isPausedByUser = m_mpvWidget->getProperty("pause").toBool();
    bool isBuffering    = m_mpvWidget->getProperty("paused-for-cache")
                           .toBool(); // True if paused due to cache running out
    bool coreIdle =
        m_mpvWidget->getProperty("core-idle")
            .toBool(); // True if playback is finished or nothing to play
    double playbackTime = m_mpvWidget->getProperty("playback-time").toDouble();

    qDebug() << "onMpvPositionChanged - Pos:" << newPosition
             << "PlaybackTime:" << playbackTime
             << "ActiveSeq:" << m_activePlaylistSequenceId
             << "ItemNumSegs:" << currentItem.num_segs()
             << "MpvDuration:" << mpvDuration
             << "PausedByUser:" << isPausedByUser << "Buffering:" << isBuffering
             << "CoreIdle:" << coreIdle;

    bool effectiveEOF = false;

    if (coreIdle &&
        playbackTime > 0) { // If core is idle and we've played something
        qInfo() << "App::onMpvPositionChanged: MPV core is idle, considering "
                   "this EOF for stream "
                << m_activePlaylistSequenceId;
        effectiveEOF = true;
    } else if (currentItem.num_segs() >
               0) { // Only proceed if we have num_segs to calculate a duration
        double calculatedDurationFromSegs =
            static_cast<double>(currentItem.num_segs()) *
            2.0; // Nominal segment duration = 2s
        double durationToCompare =
            (mpvDuration > 1.0 &&
             mpvDuration <= calculatedDurationFromSegs + 2.0)
                ? mpvDuration
                : calculatedDurationFromSegs; // Prefer mpvDuration if sane

        if (durationToCompare > 0) {
            if (isBuffering && (playbackTime >= durationToCompare - 1.0 ||
                                newPosition >= durationToCompare - 1.0)) {
                qInfo()
                    << "App::onMpvPositionChanged: Buffering near end of stream"
                    << m_activePlaylistSequenceId << ". Pos:" << newPosition
                    << ", PlaybackTime: " << playbackTime
                    << ", EffectiveDuration:" << durationToCompare;
                effectiveEOF = true;
            } else if (!isPausedByUser && !isBuffering &&
                       (newPosition >= durationToCompare - 0.2)) {
                qInfo() << "App::onMpvPositionChanged: Position very near end "
                           "of stream"
                        << m_activePlaylistSequenceId << ". Pos:" << newPosition
                        << ", EffectiveDuration:" << durationToCompare;
                effectiveEOF = true;
            }
        }
    }

    if (effectiveEOF) {
        if (!m_playbackCompletedCurrentStream) {
            qInfo() << "App::onMpvPositionChanged: Effective EOF *confirmed* "
                       "for stream"
                    << m_activePlaylistSequenceId;
            onLocalPlayerFileEnded();
        }
    }
}

void App::onLocalPlayerFileEnded() {
    // This function can be called by:
    // 1. MpvWidget emitting actualFileEnded (from MPV_EVENT_END_FILE)
    // 2. onMpvPositionChanged detecting effective EOF

    if (m_playbackCompletedCurrentStream) {
        qInfo() << "App::onLocalPlayerFileEnded: Completion already reported "
                   "for current stream"
                << m_activePlaylistSequenceId;
        return;
    }
    uint32_t endedStreamSeqId =
        getCurrentStreamSequenceId(); // Get the ID of the stream that just
                                      // ended

    qInfo() << "App::onLocalPlayerFileEnded: Reporting end of file for "
               "streamSequenceId:"
            << endedStreamSeqId;

    m_playbackCompletedCurrentStream =
        true; // Set flag HERE, as this is the point of action

    if (!m_worker) {
        qWarning() << "App::onLocalPlayerFileEnded: m_worker is null, cannot "
                      "send PlaybackStreamCompleted.";
        return;
    }

    client::ClientMsg clientMsg;
    client::p2p::PlaybackStreamCompleted* cmd =
        clientMsg.mutable_playback_stream_completed_cmd();
    cmd->set_stream_sequence_id(endedStreamSeqId);
    cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());

    qInfo() << "App::onLocalPlayerFileEnded: Sending PlaybackStreamCompleted "
               "for streamSequenceId:"
            << endedStreamSeqId;
    m_worker->send(clientMsg);
}

} // namespace gui