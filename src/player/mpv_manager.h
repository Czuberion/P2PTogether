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
#ifndef MPV_MANAGER_H
#define MPV_MANAGER_H

#include <QString>
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
class MpvManager {
public:
    /*!
     * \brief Constructs an MpvManager
     *
     * \param hlsURL The HLS URL to be played by mpv.
     */
    MpvManager(const QString& hlsURL);

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
     */
    void setStreamURL(const QString& url);

private:
    QString m_hlsURL; // Full HTTP URL for mini‑HLS endpoint.
};

} // namespace player

#endif // MPV_MANAGER_H