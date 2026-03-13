#include "mpv_manager.h"
#include <QDebug>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QRegularExpression>
#include <QTimer>
#include <QUrl>
#include <cerrno>
#include <cstring>
#include <stdexcept>
#include <sys/stat.h>
#include <sys/types.h>

namespace player {

// ────────────────────────── ctor / dtor ──────────────────────────

MpvManager::MpvManager(const QString& hlsURL, QObject* parent) :
    QObject(parent), m_hlsURL(hlsURL) {
    probeTimer_.setInterval(500);
    probeTimer_.setSingleShot(false);

    // poll every 500 ms until the manifest contains at least one segment
    connect(&probeTimer_, &QTimer::timeout, this, &MpvManager::probePlaylist);

    probeTimer_.start();
    qInfo() << "MpvManager: HLS URL set to" << m_hlsURL;
}

MpvManager::MpvManager(const std::string& hlsURL) :
    MpvManager(QString::fromStdString(hlsURL)) {}

MpvManager::~MpvManager() = default;

// ─────────────────────── getters / setters ───────────────────────

QString MpvManager::getStreamURL() const {
    return m_hlsURL;
}

bool MpvManager::hasProbedMediaSequence() const {
    return m_hasProbedMediaSequence;
}

quint32 MpvManager::probedMediaSequence() const {
    return m_probedMediaSequence;
}

void MpvManager::setStreamURL(const QString& url) {
    m_hlsURL = url;
    if (!probeTimer_.isActive())
        probeTimer_.start();
    qInfo() << "MpvManager: HLS URL updated to" << m_hlsURL;
}

// ───────────────────────── internal helpers ──────────────────────

void MpvManager::probePlaylist() {
    QNetworkRequest req {QUrl(m_hlsURL)};

    QNetworkReply* rep = nam_.get(req); // async GET

    connect(rep, &QNetworkReply::finished, this, [this, rep]() {
        const auto err  = rep->error();
        const auto body = rep->readAll();
        rep->deleteLater();

        if (err == QNetworkReply::NoError && body.contains("#EXTINF")) {
            const QString bodyStr = QString::fromUtf8(body);
            static const QRegularExpression re("#EXT-X-MEDIA-SEQUENCE:(\\d+)");
            const QRegularExpressionMatch m = re.match(bodyStr);
            if (m.hasMatch()) {
                bool ok              = false;
                const quint32 parsed = m.captured(1).toUInt(&ok);
                if (ok) {
                    m_probedMediaSequence    = parsed;
                    m_hasProbedMediaSequence = true;
                }
            }
            probeTimer_.stop(); // playlist finally ready
            tryStartPlayback();
        }
    });
}

void MpvManager::tryStartPlayback() {
    // Stop any residual polling – defensive, but cheap.
    if (probeTimer_.isActive())
        probeTimer_.stop();

    qDebug()
        << "[MpvManager] Playlist ready – emitting playlistReady() for URL:"
        << m_hlsURL;

    // Always emit if playlist is deemed ready by probe, especially for reloads
    // where URL string is same.
    Q_EMIT playlistReady(m_hlsURL);
}

} // namespace player