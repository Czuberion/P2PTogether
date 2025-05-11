#include "gui/app.h"
#include "gui/menus.h"
#include "gui/right_panel.h"
#include "gui/video_panel.h"
#include "player/mpv_manager.h"
#include "roles/role_store.h"
#include "transport/control_stream_worker.h"
#include "transport/grpc_client.h"

#include <QDebug>
#include <QHBoxLayout>
#include <QMetaObject>
#include <QSplitter>
#include <QThread>
#include <QWidget>
#include <stdexcept>

namespace gui {

int runGUI(P2P::Peer* peer, quint16 grpcPort) {
    // Qt sets the locale in the QApplication constructor, but libmpv requires
    // the LC_NUMERIC category to be set to "C", so change it back.
    setlocale(LC_NUMERIC, "C");

    // Add a small delay to allow the peer_service gRPC server to fully start
    // This is a simple workaround for potential race conditions on startup.
    QThread::msleep(500); // Wait for 500 milliseconds

    // --- Instantiate gRPC Client pointed at the dynamic port  ---
    P2P::GrpcClient grpcClient(
        QString("127.0.0.1:%1").arg(grpcPort).toStdString());
    quint32 hlsPort = 0;
    try {
        client::ServiceInfo serviceInfo = grpcClient.getServiceInfo();
        hlsPort                         = serviceInfo.hls_port();
        qInfo() << "Successfully fetched HLS port from peer service:"
                << hlsPort;
    } catch (const std::runtime_error& e) {
        qCritical() << "Failed to get service info from peer_service:"
                    << e.what();
        return -1;
    }

    if (hlsPort == 0) {
        qCritical() << "Received invalid HLS port (0) from peer service.";
        return -1;
    }

    // --- Create MpvManager with the HLS URL provided by the Peer Service ---
    QString hlsUrl = QString("http://127.0.0.1:%1/stream.m3u8").arg(hlsPort);
    player::MpvManager mpvManager(hlsUrl);

    // --- Instantiate RoleStore ---
    P2P::Roles::RoleStore roleStore;

    // --- Placeholder for our actual Peer ID, to be received from service ---
    // The 'peer' object passed to runGUI is mostly a dummy for roles for now.
    // We'll update its peerId field when we get the LocalPeerIdentity.

    // --- Main Window Setup ---
    QMainWindow window;
    window.setWindowTitle("P2PTogether");
    window.resize(1200, 700);

    // -------- control-stream worker thread ----------
    // Create thread and worker without parents initially
    QThread* netThread = new QThread();
    auto worker        = new P2P::ControlStreamWorker(
        std::make_unique<P2P::GrpcClient>(std::move(grpcClient)), nullptr);

    // ---- observe the worker for debugging / UX feedback ----
    QObject::connect(worker, &P2P::ControlStreamWorker::finished, &window,
                     [](const QString& reason) {
                         qWarning() << "[control‑stream]" << reason;
                     });

    // --- Connect ControlStreamWorker::serverMsg to RoleStore and other
    // handlers ---
    QObject::connect(
        worker, &P2P::ControlStreamWorker::serverMsg, &window,
        [&roleStore, peer](const client::ServerMsg& msg) {
            qDebug() << "[control‑stream] received payload type"
                     << msg.payload_case();
            // Delegate to RoleStore for role-related messages
            switch (msg.payload_case()) {
            case client::ServerMsg::kRoleDefinitionsUpdate:
                // Use QMetaObject::invokeMethod if roleStore might
                // be in a different thread or if its methods are
                // not reentrant and worker is on another thread.
                // For now, assuming direct call is okay if signals
                // are queued or same thread. QueuedConnection on
                // the original connect() helps here.
                roleStore.processRoleDefinitionsUpdate(
                    msg.role_definitions_update());
                break;
            case client::ServerMsg::kPeerRoleAssignment:
                roleStore.processPeerRoleAssignment(msg.peer_role_assignment());
                break;
            case client::ServerMsg::kAllPeerRoleAssignments:
                roleStore.processAllPeerAssignments(
                    msg.all_peer_role_assignments());
                break;
            case client::ServerMsg::kLocalPeerIdentity:
                roleStore.setLocalPeerId(QString::fromStdString(
                    msg.local_peer_identity().peer_id()));
                // The dummy 'peer' object is less important now for ID.
                // RoleStore holds the true one.
                qInfo() << "GUI: Received LocalPeerIdentity, my ID is now:"
                        << roleStore.getLocalPeerId();
                // TODO: May need to trigger UI updates that depend on knowing
                // the true local peer ID. For example, refresh role menus or
                // permission-gated elements if they were initialized before
                // this ID was known. Or connect to a new signal from P2P::Peer.
                break;
            // TODO: Add cases for kQueueUpdate to update
            // QListWidget (as done previously) and other ServerMsg
            // types.
            default: break;
            }
        });

    worker->moveToThread(netThread);

    // When thread starts, run the worker's main function
    QObject::connect(netThread, &QThread::started, worker,
                     &P2P::ControlStreamWorker::start);

    // When worker finishes processing, tell the thread's event loop to quit
    QObject::connect(worker, &P2P::ControlStreamWorker::finished, netThread,
                     &QThread::quit);

    // When the thread finishes (after quit() and run() returns), schedule
    // worker deletion
    QObject::connect(netThread, &QThread::finished, worker,
                     &QObject::deleteLater);

    // When the thread finishes, schedule the thread object itself for deletion
    QObject::connect(netThread, &QThread::finished, netThread,
                     &QObject::deleteLater);

    // --- Application Cleanup ---
    QObject::connect(
        QApplication::instance(), &QApplication::aboutToQuit,
        [peer, worker]() { // Capture worker by pointer
            qDebug() << "GUI about to quit; initiating cleanup...";
            // Request the worker to stop (will run in netThread)
            // Use QueuedConnection so it posts an event to the netThread's loop
            QMetaObject::invokeMethod(worker, "stop", Qt::QueuedConnection);
            peer->cleanup(); // Call peer cleanup (runs in main thread)
            qDebug() << "Peer cleanup requested.";
            // Don't wait here; signals handle thread termination.
        });

    netThread->start(); // Start the thread's event loop

    // Create central widget and main layout
    QWidget* centralWidget = new QWidget(&window);
    window.setCentralWidget(centralWidget);
    QHBoxLayout* mainLayout = new QHBoxLayout(centralWidget);
    centralWidget->setLayout(mainLayout);
    mainLayout->setContentsMargins(0, 0, 0, 0);
    mainLayout->setSpacing(0);

    // Main splitter
    QSplitter* mainSplitter = new QSplitter(Qt::Horizontal, centralWidget);
    mainLayout->addWidget(mainSplitter);

    // Left panel: video playback (pass MpvManager and window)
    QWidget* leftPanel = createVideoPanel(&mpvManager, &window);
    if (!leftPanel) { // Handle potential errors from createVideoPanel
        qCritical() << "Failed to create video panel!";
        // Consider showing an error message
        return -1;
    }
    mainSplitter->addWidget(leftPanel);

    // Right panel: chat, queue, etc.
    QWidget* rightPanel = createRightPanel(peer, &window, worker, &roleStore);
    mainSplitter->addWidget(rightPanel);
    rightPanel->setMinimumWidth(300);

    // Set splitter sizes: 65% left, 35% right
    QList<int> sizes;
    sizes << int(window.width() * 0.65) << int(window.width() * 0.35);
    mainSplitter->setSizes(sizes);
    mainSplitter->setCollapsible(0, false); // Keep video panel always visible

    // Create menus
    createMenus(&window, peer, &roleStore, mainSplitter, rightPanel, leftPanel,
                worker);

    window.show();
    // Return the exit code from the application event loop
    int rc = QApplication::exec();

    qDebug() << "QApplication::exec() finished with code:" << rc;

    return rc;
}

} // namespace gui