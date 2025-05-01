/*!
 * \file main.cpp
 * \brief Entry point for the P2PTogether application.
 * \details This file contains the main function that initializes the Qt
 * application, sets up the GUI, and starts the Go service for P2P.
 *
 * \note The peer ID is currently hardcoded. GUI uses a Peer dummy class instead
 * of the Go service. The real functionality is to be provided by the Go daemon.
 *
 * \see gui/app.h
 */

#include "gui/app.h"
#include "p2p/peer.h"
#include <QApplication>
#include <QCoreApplication>
#include <QDebug>
#include <QDir>
#include <QFileInfo>
#include <QProcess>
#include <QTextStream>
#include <clocale>

int main(int argc, char* argv[]) {
    // Set up Qt Application context first
    QApplication app(argc, argv);
    QCoreApplication::setApplicationName("P2PTogether");
    QCoreApplication::setApplicationVersion("0.1");

    // Determine path to the Go peer_service executable
    // Assumes peer_service is in the same directory as P2PTogether
    QString appPath = QCoreApplication::applicationFilePath();
    QFileInfo appInfo(appPath);
    QString binDir =
        appInfo.absolutePath(); // Directory containing the executable
    QString peerServicePath = binDir + QDir::separator() + "peer_service";

    // Check if the peer_service executable exists
    if (!QFileInfo::exists(peerServicePath)) {
        qCritical() << "Peer service executable not found at:"
                    << peerServicePath;
        qCritical() << "Please ensure the peer_service target has been built.";
        return 1; // Exit if service cannot be found
    }

    // Start the Go peer_service
    QProcess peerServiceProcess;
    // Redirect output channels
    peerServiceProcess.setProcessChannelMode(QProcess::MergedChannels);
    peerServiceProcess.setReadChannel(QProcess::StandardOutput);

    // Connect signals to read output when available
    QObject::connect(&peerServiceProcess, &QProcess::readyReadStandardOutput,
                     [&]() {
                         QTextStream stream(&peerServiceProcess);
                         while (!stream.atEnd()) {
                             QString line = stream.readLine();
                             qInfo() << "[peer_service]" << line;
                         }
                     });
    QObject::connect(&peerServiceProcess, &QProcess::errorOccurred,
                     [&](QProcess::ProcessError error) {
                         qCritical() << "[peer_service process error]" << error
                                     << peerServiceProcess.errorString();
                     });

    QStringList args = {"--grpc-port=8268"};
    qInfo() << "Starting peer_service at:" << peerServicePath
            << "with args:" << args;
    peerServiceProcess.start(peerServicePath, args);

    // Wait a short moment to allow the service to start and check for errors
    if (!peerServiceProcess.waitForStarted(1000)) { // Wait up to 1 second
        qCritical() << "Failed to start peer service process:"
                    << peerServiceProcess.errorString();
        // Attempt to read any error output before exiting
        QByteArray errorOutput = peerServiceProcess.readAllStandardError();
        if (!errorOutput.isEmpty()) {
            qCritical() << "[peer_service stderr on startup failure]:"
                        << QString::fromUtf8(errorOutput);
        }
        return 1; // Exit if service failed to start
    }
    qInfo() << "Started peer_service process.";

    // TODO: Replace with proper peer handling by the Go service.
    std::string peerId = "defaultPeerId";
    if (peerId.empty()) {
        qWarning() << "Peer ID is empty, using default. Pipe creation might "
                      "fail or collide.";
        peerId = "errorPeerId";
    }

    // Construct the Peer object with the chosen peer ID.
    P2P::Peer peer(peerId);
    // Launch the main Qt GUI event loop.
    int exitCode = gui::runGUI(&peer); // runGUI should return the exec() code

    // Ensure the peer service is terminated when the GUI exits
    qInfo() << "Requesting peer_service termination..."; // Add log
    peerServiceProcess.terminate(); // Sends SIGTERM

    // Wait longer for graceful shutdown (e.g., 10 seconds)
    if (!peerServiceProcess.waitForFinished(10000)) { // Wait up to 10 seconds
        qWarning() << "Peer service process did not terminate gracefully after 10 seconds. Forcing kill.";
        peerServiceProcess.kill(); // Force kill if still running
        peerServiceProcess.waitForFinished(1000); // Short wait after kill
    } else {
        qInfo() << "Peer service process terminated gracefully."; // Add log
    }

    qInfo() << "Exiting main application."; // Add log
    return exitCode;
}