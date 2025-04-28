#include "mpv_manager.h"
#include <QDebug>
#include <cerrno>
#include <cstring>
#include <stdexcept>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

namespace player {
MpvManager::MpvManager(const QString& hlsURL) : m_hlsURL(hlsURL) {
    qInfo() << "MpvManager: HLS URL set to" << m_hlsURL;
}

MpvManager::MpvManager(const std::string& hlsURL) :
    m_hlsURL(QString::fromStdString(hlsURL)) {
    qInfo() << "MpvManager: HLS URL set to" << m_hlsURL;
}

MpvManager::~MpvManager() = default;

QString MpvManager::getStreamURL() const {
    return m_hlsURL;
}

void MpvManager::setStreamURL(const QString& url) {
    m_hlsURL = url;
    qInfo() << "MpvManager: HLS URL updated to" << m_hlsURL;
}

} // namespace player