#include "mpv_manager.h"
#include <QDebug>
#include <QDir>
#include <cerrno>
#include <cstring>
#include <stdexcept>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

namespace player {

MpvManager::MpvManager(const std::string& peerId) : m_peerId(peerId) {
    // Construct the pipe path in /tmp/
    m_pipePath = QDir::tempPath() +
                 QString("/p2p2_%1").arg(QString::fromStdString(m_peerId));
    qInfo() << "MpvManager: Pipe path set to" << m_pipePath;
    if (!setupPipe()) {
        // Consider throwing an exception or handling the error more robustly
        qCritical()
            << "MpvManager: Failed to create FIFO pipe on construction.";
        // throw std::runtime_error("Failed to create FIFO pipe");
    }
}

MpvManager::~MpvManager() {
    cleanupPipe();
}

bool MpvManager::setupPipe() {
    if (m_pipeCreated) {
        qWarning() << "MpvManager: Pipe already created.";
        return true;
    }

    // Use POSIX mkfifo to create the named pipe
    // Permissions: 0660 (user/group read/write)
    if (mkfifo(m_pipePath.toStdString().c_str(), 0660) == 0) {
        qInfo() << "MpvManager: Successfully created FIFO pipe:" << m_pipePath;
        m_pipeCreated = true;
        return true;
    } else {
        // Check if the error is because the file already exists
        if (errno == EEXIST) {
            qWarning() << "MpvManager: Pipe already exists (possibly from "
                          "previous run):"
                       << m_pipePath;
            // Attempt to remove the old pipe first, then try creating again
            cleanupPipe(); // Try to remove it
            if (mkfifo(m_pipePath.toStdString().c_str(), 0660) == 0) {
                qInfo() << "MpvManager: Successfully created FIFO pipe after "
                           "removing old one:"
                        << m_pipePath;
                m_pipeCreated = true;
                return true;
            } else {
                qCritical() << "MpvManager: Failed to create FIFO pipe after "
                               "removing old one:"
                            << m_pipePath << "Error:" << strerror(errno);
                m_pipeCreated = false;
                return false;
            }
        } else {
            qCritical() << "MpvManager: Failed to create FIFO pipe:"
                        << m_pipePath << "Error:" << strerror(errno);
            m_pipeCreated = false;
            return false;
        }
    }
}

void MpvManager::cleanupPipe() {
    if (m_pipeCreated ||
        QFileInfo::exists(m_pipePath)) { // Check if it exists even if creation
                                         // failed but file remained
        qInfo() << "MpvManager: Cleaning up FIFO pipe:" << m_pipePath;
        if (unlink(m_pipePath.toStdString().c_str()) != 0) {
            qWarning() << "MpvManager: Failed to remove FIFO pipe:"
                       << m_pipePath << "Error:" << strerror(errno);
        }
        m_pipeCreated = false; // Mark as not created anymore
    } else {
        qDebug() << "MpvManager: Cleanup requested, but pipe was not created "
                    "or doesn't exist.";
    }
}

QString MpvManager::getPipePath() const {
    return m_pipePath;
}

} // namespace player