#include "gui/app.h"
#include "gui/menus.h"
#include "gui/right_panel.h"
#include "gui/video_panel.h"
#include "player/mpv_manager.h"
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

    QObject::connect(worker, &P2P::ControlStreamWorker::serverMsg, &window,
                     [](const client::ServerMsg& msg) {
                         qDebug() << "[control‑stream] received payload type"
                                  << msg.payload_case();
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
    QWidget* rightPanel = createRightPanel(peer, &window);
    mainSplitter->addWidget(rightPanel);
    rightPanel->setMinimumWidth(300);

    // Set splitter sizes: 65% left, 35% right
    QList<int> sizes;
    sizes << int(window.width() * 0.65) << int(window.width() * 0.35);
    mainSplitter->setSizes(sizes);
    mainSplitter->setCollapsible(0, false); // Keep video panel always visible

    // Create menus
    createMenus(&window, peer, mainSplitter, rightPanel, leftPanel);

    window.show();
    // Return the exit code from the application event loop
    int rc = QApplication::exec();

    qDebug() << "QApplication::exec() finished with code:" << rc;

    return rc;
}

} // namespace gui