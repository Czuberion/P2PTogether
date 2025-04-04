#ifndef MPV_MANAGER_H
#define MPV_MANAGER_H

#include <QString>
#include <string>

// Forward declaration
namespace player {
class MpvWidget;
}

namespace player {

class MpvManager {
public:
    MpvManager(const std::string& peerId);
    ~MpvManager();

    // Deleted copy/move operations
    MpvManager(const MpvManager&)            = delete;
    MpvManager& operator=(const MpvManager&) = delete;
    MpvManager(MpvManager&&)                 = delete;
    MpvManager& operator=(MpvManager&&)      = delete;

    bool setupPipe();
    void cleanupPipe();
    QString getPipePath() const;

private:
    std::string m_peerId;
    QString m_pipePath;
    bool m_pipeCreated = false;
};

} // namespace player

#endif // MPV_MANAGER_H