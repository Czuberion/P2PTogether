/*!
 * \file
 * \brief Declares the MpvManager class, a Qt helper for controlling the mpv
 * video player.
 *
 * \warning This class currently uses a platform-specific named pipe (FIFO) for
 * mpv control, which is a **temporary solution** for initial development and
 * testing. The final architecture will use gRPC communication between the GUI
 * and the Go Peer Service to manage mpv playback, making this specific pipe
 * implementation obsolete.
 *
 * \see PRD section 6
 * \see SRS 2.1, 3.1.3
 */
#pragma once

#include <QNetworkAccessManager>
#include <QObject>
#include <QString>
#include <QTimer>
#include <string>

// Forward declaration for dependency
namespace player {
class MpvWidget;
}

namespace player {

/*!
 * \class MpvManager
 * \brief Provides a Qt-friendly interface for controlling the mpv video player
 * from the GUI (**temporary implementation**).
 *
 * MpvManager abstracts the details of launching, connecting to, and sending
 * commands to mpv via a named pipe. This allows the GUI to load and control
 * video streams during early development.
 *
 * \note In the target architecture, the Go Peer Service will manage the HLS
 * stream and potentially the mpv process lifecycle. The GUI will send control
 * commands (play, pause, seek) via gRPC to the Peer Service, which will then
 * interact with mpv or manage the stream accordingly. This class will likely be
 * refactored or replaced.
 */
class MpvManager : public QObject {
    Q_OBJECT
public:
    /*!
     * \brief Constructs an MpvManager
     *
     * \param hlsURL The HLS URL to be played by mpv.
     * \param parent Lets Qt manage lifetime if you embed this in a widget
     */
    explicit MpvManager(const QString& hlsURL, QObject* parent = nullptr);

    /*!
     * \brief Constructs an MpvManager
     *
     * \param hlsURL The HLS URL to be played by mpv.
     */
    MpvManager(const std::string& hlsURL);

    ~MpvManager();

    // Deleted copy/move operations to prevent accidental duplication.
    MpvManager(const MpvManager&)            = delete;
    MpvManager& operator=(const MpvManager&) = delete;
    MpvManager(MpvManager&&)                 = delete;
    MpvManager& operator=(MpvManager&&)      = delete;

    /*!
     * \brief Returns the HLS URL for the stream to be played.
     * \return QString containing the HLS URL.
     */
    QString getStreamURL() const;

    /*!
     * \brief Sets the HLS URL for the stream to be played.
     * \param url The new HLS URL to be set.
     *
     * This will start a 500 ms polling timer that watches the playlist
     * until it contains at least one real segment (`#EXTINF:`),
     * at which point `tryStartPlayback()` is invoked to actually load mpv.
     */
    void setStreamURL(const QString& url);

private:
    QString m_hlsURL; ///< Full HTTP URL for mini‑HLS endpoint.

    // ——— Added for HLS “empty playlist” probe ———
    QNetworkAccessManager nam_; ///< used to fetch the playlist
    QTimer probeTimer_;         ///< polls every 500 ms

    /// Timer callback that fetches the playlist and looks for “#EXTINF:”
    void probePlaylist();

    /// Once we see “#EXTINF:” in the playlist, stop polling and actually call
    /// the mpv loadfile command.
    void tryStartPlayback();

Q_SIGNALS:
    /*!
     * \brief Emitted exactly once when the playlist finally contains at least
     *        one `#EXTINF:` segment line – i.e. mpv can start playback.
     *
     * Connect this to something like
     * `mpvWidget->command(QStringList{"loadfile", url});`
     */
    void playlistReady(const QString& url);
};

} // namespace player
