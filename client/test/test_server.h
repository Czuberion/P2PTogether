#pragma once

#include <QJsonObject>
#include <QMainWindow>
#include <QPointer>
#include <QTcpServer>
#include <QTimer>
#include <QtTest/QTest>

class QTcpSocket;

namespace gui {
class App;
}

// Structure to track pending event waiters
struct PendingEventWaiter {
  QPointer<QTcpSocket> socket;
  QString eventType;
  QTimer *timeoutTimer = nullptr;
};

class TestServer : public QTcpServer {
  Q_OBJECT
public:
  TestServer(gui::App *app, QMainWindow *window, quint16 port);

  // Called by GUI (e.g., App) to notify that an event occurred
  // This will wake up any waiting command for this event type
  void registerEvent(const QString &eventType, const QJsonObject &data);

private slots:
  void onNewConnection();
  void onReadyRead();

private:
  gui::App *m_app;
  QMainWindow *m_window;

  // Event waiting infrastructure
  QList<PendingEventWaiter> m_pendingWaiters;
  QMap<QString, QJsonObject> m_eventQueue; // Events that arrived before waiter

  // Automation primitives
  QJsonObject executeCommand(const QJsonObject &cmd);
  bool clickWidget(const QString &name);
  bool typeText(const QString &name, const QString &text);
  bool pressKey(const QString &name, const QString &keyName);
  bool clickDialogButton(const QString &name, const QString &button);
  bool triggerMenuAction(const QString &menuName, const QString &actionName);
  QString getText(const QString &name);
  void waitForEvent(const QString &eventType, int timeoutMs,
                    QTcpSocket *socket);

  // Helper to find widgets recursively if needed, though findChild is usually
  // sufficient
};
