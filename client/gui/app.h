/*!
 * \file
 * \brief Declares the entry point for launching the Qt GUI for P2PTogether.
 *
 * The GUI is the user-facing component of P2PTogether, a fully P2P desktop
 * application for synchronized video streaming and interaction. The GUI
 * interacts with the Peer Service (Go) via gRPC and provides session
 * management, playback, chat, queue, and analytics features.
 *
 * \see gui/menus.h
 * \see gui/right_panel.h
 * \see gui/video_panel.h
 * \see player/mpv_manager.h
 */
#pragma once

#include "p2p/peer.h"
#include "p2p/queue.pb.h"
#include "player/mpv_manager.h"
#include "player/mpvwidget.h"
#include "roles/role_store.h"
#include "transport/control_stream_worker.h"

#include <QApplication>
#include <QMainWindow>
#include <QPushButton>
#include <QString>

class TestServer; // Forward declaration

namespace gui {

/*!
 * \brief The main application class for P2PTogether GUI.
 *
 * This class manages the main application window, P2P connection, and UI
 * components. It handles the lifecycle of the application and provides
 * methods to set up the GUI and manage events.
 */
class App : public QObject { // Make App a QObject
  Q_OBJECT
public:
  explicit App(quint16 grpcPort, QObject *parent = nullptr);
  ~App();

  int exec(); // Renamed from runGUI to match QApplication::exec

  // Test mode configuration
  void enableTestMode(quint16 port);
  TestServer *testServer() const { return m_testServer; }

  double playlistOriginSec() const { return m_playlistOriginSec; }

  // Methods for video_panel to query queue state for button enablement
  QList<client::p2p::QueueItem> getCurrentQueueItems() const;
  int getCurrentPlayingIndex() const;
  uint32_t getCurrentStreamSequenceId() const;
  // Session state management
  QString getCurrentSessionId() const;
  QString getCurrentInviteCode() const;
  void setActiveSessionDetails(const QString &sessionId,
                               const QString &inviteCode);
  void clearActiveSessionDetails();

  // Method to programmatically add video to queue (used by TestServer)
  void addVideoToQueue(const QString &filePath);

private slots:
  // Slot to confirm netThread has started
  void onNetThreadStarted();
  void onServerMessage(const client::ServerMsg &msg);
  void onMpvPositionChanged(double newPosition);
  void onLocalPlayerFileEnded();

private:
  quint16 m_grpcPort;

  // Test mode state
  bool m_testModeEnabled = false;
  quint16 m_testPort = 0;
  TestServer *m_testServer = nullptr;

  QString m_localUsername;
  P2P::Roles::RoleStore *m_roleStore = nullptr;
  P2P::ControlStreamWorker *m_worker = nullptr;
  QMainWindow *m_mainWindow = nullptr;
  player::MpvWidget *m_mpvWidget = nullptr;
  player::MpvManager *m_mpvManager = nullptr;
  QPushButton *m_playPauseBtn = nullptr;
  // QSlider* m_seekSlider = nullptr; // For later

  // Store last applied HLC for playback state
  int64_t m_lastAppliedPlaybackHlc = -1;
  double m_playlistOriginSec =
      0.0; // Origin of current HLS content relative to item start
  bool m_isCleaningUp = false; // Flag to prevent re-entrant cleanup
  QMetaObject::Connection m_seekOnLoadConnection;

  // State for skip buttons
  QList<client::p2p::QueueItem> m_currentQueueItems;
  int m_currentPlayingIndex = -1; // Index in m_currentQueueItems
  // Sequence ID from the latest PlaylistReset
  uint32_t m_activePlaylistSequenceId = 0;
  bool m_playbackCompletedCurrentStream = false; // Prevent multiple EOF reports

  // Active session details
  QString m_currentSessionId;
  QString m_currentInviteCode;

  void setupP2P();
  void setupUI();
  void cleanup();

signals:
  // Emitted when queue items or playing index changes
  void queueStateChanged();
  void sessionStateChanged(bool isActive);
  void sessionCreatedSuccessfully(const QString &sessionID,
                                  const QString &inviteCode);
  void sessionCreationFailed(const QString &errorMessage);
  void chatMessageReceived(const QString &senderName,
                           const QString &messageText, bool isSelf);
  // UI updates
};

} // namespace gui
