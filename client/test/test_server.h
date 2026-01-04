#pragma once

#include <QJsonObject>
#include <QMainWindow>
#include <QTcpServer>
#include <QtTest/QTest>

class TestServer : public QTcpServer {
  Q_OBJECT
public:
  TestServer(QMainWindow *window, quint16 port);

private slots:
  void onNewConnection();
  void onReadyRead();

private:
  QMainWindow *m_window;

  // Automation primitives
  QJsonObject executeCommand(const QJsonObject &cmd);
  bool clickWidget(const QString &name);
  bool typeText(const QString &name, const QString &text);
  bool pressKey(const QString &name, const QString &keyName);
  bool clickDialogButton(const QString &name, const QString &button);
  bool triggerMenuAction(const QString &menuName, const QString &actionName);

  // Helper to find widgets recursively if needed, though findChild is usually
  // sufficient
};
