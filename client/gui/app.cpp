#include "gui/app.h"
#include "gui/menus.h"
#include "gui/right_panel.h"
#include "gui/video_panel.h"
#include "player/mpv_manager.h"

#include <QDebug>
#include <QHBoxLayout>
#include <QSplitter>
#include <QWidget>
#include <stdexcept>

namespace gui {

void runGUI(P2P::Peer* peer) {
    int argc = 0; // QApplication doesn't need real args here if not used
    QApplication app(argc, nullptr);
    app.setApplicationName("P2PTogether");
    app.setApplicationDisplayName("P2PTogether");

    // Qt sets the locale in the QApplication constructor, but libmpv requires
    // the LC_NUMERIC category to be set to "C", so change it back.
    setlocale(LC_NUMERIC, "C");

    // --- Create MpvManager with the HLS URL provided by the Peer Service ---
    // (Assumes you've already done a GetServiceInfo RPC to fetch hlsPort.)
    quint32 hlsPort = /* grpcClient.GetServiceInfo(...).hls_port */ 9901;
    QString hlsUrl  = QString("http://127.0.0.1:%1/stream.m3u8").arg(hlsPort);
    player::MpvManager mpvManager(hlsUrl);

    // --- Main Window Setup ---
    QMainWindow window;
    window.setWindowTitle("P2PTogether");
    window.resize(1200, 700);

    // --- Cleanup Connections ---
    QObject::connect(&app, &QApplication::aboutToQuit, [peer, &mpvManager]() {
        qDebug() << "GUI window closing; cleaning up resources...";
        // mpvManager.cleanupPipe(); // Destructor will handle this now
        peer->cleanup();
        qDebug() << "Cleanup finished.";
    });
    // Note: MpvManager destructor runs when 'runGUI' scope ends after
    // app.exec() returns.

    // Create central widget and main layout
    QWidget* centralWidget = new QWidget(&window);
    window.setCentralWidget(centralWidget);
    QHBoxLayout* mainLayout = new QHBoxLayout(centralWidget);
    centralWidget->setLayout(mainLayout);

    // Main splitter
    QSplitter* mainSplitter = new QSplitter(Qt::Horizontal, centralWidget);
    mainLayout->addWidget(mainSplitter);

    // Left panel: video playback (pass MpvManager and window)
    QWidget* leftPanel = createVideoPanel(&mpvManager, &window);
    if (!leftPanel) { // Handle potential errors from createVideoPanel
        qCritical() << "Failed to create video panel!";
        // Consider showing an error message
        return;
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
    app.exec();

    // MpvManager destructor is automatically called here when it goes out of
    // scope
}

} // namespace gui