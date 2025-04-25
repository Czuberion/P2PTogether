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
     * \brief Constructs an MpvManager for a given peer ID, creating a unique
     * pipe path.
     * \param peerId The unique identifier for the peer (used in pipe naming).
     * \note The peer ID usage here is part of the temporary pipe mechanism.
     */
    MpvManager(const std::string& peerId);
    /*!
     * \brief Destructor. Cleans up the named pipe if it was created.
     * \note Relevant only for the temporary pipe implementation.
     */
    ~MpvManager();

    // Deleted copy/move operations to prevent accidental duplication.
    MpvManager(const MpvManager&)            = delete;
    MpvManager& operator=(const MpvManager&) = delete;
    MpvManager(MpvManager&&)                 = delete;
    MpvManager& operator=(MpvManager&&)      = delete;

    /*!
     * \brief Creates the named pipe if it does not already exist
     * (**temporary**).
     * \return true if the pipe was created or already exists, false on error.
     * \warning Uses POSIX-specific `mkfifo`, not cross-platform.
     */
    bool setupPipe();
    /*!
     * \brief Removes the named pipe if it exists (**temporary**).
     * \warning Uses POSIX-specific `unlink`.
     */
    void cleanupPipe();
    /*!
     * \brief Returns the full path to the named pipe for use by mpv
     * (**temporary**).
     * \return QString containing the pipe path.
     */
    QString getPipePath() const;

private:
    std::string m_peerId; ///< Peer ID used for temporary pipe naming.
    QString m_pipePath;   ///< Full path to the temporary named pipe.
    bool m_pipeCreated =
        false; ///< True if the temporary pipe was created by this manager.
};

} // namespace player

#endif // MPV_MANAGER_H