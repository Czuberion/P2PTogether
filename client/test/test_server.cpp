#include "test_server.h"
#include <QApplication>
#include <QDialogButtonBox>
#include <QJsonDocument>
#include <QLineEdit>
#include <QMenuBar>
#include <QPointer>
#include <QPushButton>
#include <QTcpSocket>
#include <QTimer>

TestServer::TestServer(QMainWindow *window, quint16 port) : m_window(window) {
  if (this->listen(QHostAddress::Any, port)) {
    connect(this, &QTcpServer::newConnection, this,
            &TestServer::onNewConnection);
  } else {
    qWarning() << "TestServer failed to listen on port" << port;
  }
}

void TestServer::onNewConnection() {
  QTcpSocket *clientConnection = this->nextPendingConnection();
  connect(clientConnection, &QTcpSocket::readyRead, this,
          &TestServer::onReadyRead);
  connect(clientConnection, &QTcpSocket::disconnected, clientConnection,
          &QTcpSocket::deleteLater);
}

void TestServer::onReadyRead() {
  QTcpSocket *rawSocket = qobject_cast<QTcpSocket *>(sender());
  if (!rawSocket)
    return;

  // Read lines synchronously but execute commands asynchronously
  while (rawSocket->canReadLine()) {
    QByteArray line = rawSocket->readLine().trimmed();
    if (line.isEmpty())
      continue;

    QJsonDocument doc = QJsonDocument::fromJson(line);
    if (doc.isObject()) {
      QJsonObject commandObj = doc.object();
      QPointer<QTcpSocket> safeSocket(rawSocket);

      // Defer execution using QTimer to avoid blocking in readyRead slot.
      // This protects against Use-After-Free crashes if the peer closes the
      // connection while we are blocked in a nested loop (e.g. modal dialog).
      QTimer::singleShot(0, this, [this, safeSocket, commandObj]() {
        QJsonObject response = executeCommand(commandObj);

        if (safeSocket) {
          safeSocket->write(
              QJsonDocument(response).toJson(QJsonDocument::Compact) + "\n");
          safeSocket->flush();
        } else {
          // Socket dead, can't send response. This is expected if peer closed
          // connection.
        }
      });
    }
  }
}

bool TestServer::clickWidget(const QString &name) {
  // Search in the main window
  QWidget *widget = m_window->findChild<QWidget *>(name);
  // If not found in main window, try searching all top-level widgets (for modal
  // dialogs)
  if (!widget) {
    for (QWidget *topLevel : QApplication::topLevelWidgets()) {
      if (topLevel->isVisible()) {
        widget = topLevel->findChild<QWidget *>(name);
        if (widget)
          break;
      }
    }
  }

  if (!widget) {
    qWarning() << "TestServer: Widget not found:" << name;
    return false;
  }

  if (!widget->isVisible()) {
    qWarning() << "TestServer: Widget found but not visible:" << name;
    // Try to click anyway? No, QTest usually requires visibility.
    // return false;
  }

  qInfo() << "TestServer: Clicking widget" << name;
  QTest::mouseClick(widget, Qt::LeftButton);
  return true;
}

bool TestServer::typeText(const QString &name, const QString &text) {
  QWidget *widget = m_window->findChild<QWidget *>(name);
  if (!widget) {
    // Check top levels for dialogs
    for (QWidget *topLevel : QApplication::topLevelWidgets()) {
      if (topLevel->isVisible()) {
        widget = topLevel->findChild<QWidget *>(name);
        if (widget)
          break;
      }
    }
  }

  if (!widget) {
    qWarning() << "TestServer: Widget not found for typing:" << name;
    return false;
  }

  // Check if it handles text (like a line edit) and set focus
  QLineEdit *edit = qobject_cast<QLineEdit *>(widget);
  if (edit) {
    edit->setFocus();
    edit->clear(); // Clear before typing?
                   // Or maybe strictly "type" appends? Let's just keyClicks.
    // QTest::keyClicks appends. To clear we might need separate command or just
    // set text. For simulation, setFocus + keyClicks is most realistic.
  }

  widget->setFocus();
  QTest::keyClicks(widget, text);
  return true;
}

bool TestServer::pressKey(const QString &name, const QString &keyName) {
  QWidget *widget = m_window->findChild<QWidget *>(name);
  if (!widget) {
    for (QWidget *topLevel : QApplication::topLevelWidgets()) {
      if (topLevel->isVisible()) {
        widget = topLevel->findChild<QWidget *>(name);
        if (widget)
          break;
      }
    }
  }

  if (!widget) {
    qWarning() << "TestServer: Widget not found for key press:" << name;
    return false;
  }

  Qt::Key key = Qt::Key_unknown;
  if (keyName == "Enter" || keyName == "Return")
    key = Qt::Key_Return;
  else if (keyName == "Esc")
    key = Qt::Key_Escape;
  else if (keyName == "Tab")
    key = Qt::Key_Tab;
  else if (keyName == "Space")
    key = Qt::Key_Space;
  else if (keyName == "Backspace")
    key = Qt::Key_Backspace;

  if (key == Qt::Key_unknown) {
    qWarning() << "TestServer: Unknown key:" << keyName;
    return false;
  }

  widget->setFocus();
  qInfo() << "TestServer: Pressing key" << keyName << "on" << name;
  QTest::keyClick(widget, key);
  return true;
}

bool TestServer::clickDialogButton(const QString &name,
                                   const QString &buttonRole) {
  qInfo() << "TestServer::clickDialogButton - START. Widget:" << name
          << "Button:" << buttonRole;

  QDialogButtonBox *buttonBox = m_window->findChild<QDialogButtonBox *>(name);
  if (!buttonBox) {
    qInfo() << "TestServer::clickDialogButton - Not found in main window, "
               "searching top-level widgets...";
    for (QWidget *topLevel : QApplication::topLevelWidgets()) {
      if (topLevel->isVisible()) {
        buttonBox = topLevel->findChild<QDialogButtonBox *>(name);
        if (buttonBox) {
          qInfo()
              << "TestServer::clickDialogButton - Found in top-level widget:"
              << topLevel->windowTitle();
          break;
        }
      }
    }
  }

  if (!buttonBox) {
    qWarning() << "TestServer::clickDialogButton - DialogButtonBox not found:"
               << name;
    return false;
  }

  QDialogButtonBox::StandardButton btn = QDialogButtonBox::NoButton;
  if (buttonRole == "Ok")
    btn = QDialogButtonBox::Ok;
  else if (buttonRole == "Cancel")
    btn = QDialogButtonBox::Cancel;

  QPushButton *pushBtn = buttonBox->button(btn);
  if (!pushBtn) {
    qWarning() << "TestServer::clickDialogButton - Button not found in box:"
               << buttonRole;
    return false;
  }

  qInfo() << "TestServer::clickDialogButton - Found button, scheduling "
             "deferred click...";

  // Use QTimer::singleShot to defer the click until after we return from
  // the current event handler. This is critical because clicking a dialog
  // button closes the modal dialog's nested event loop, and doing so
  // synchronously while inside that event loop can corrupt Qt's state.
  QTimer::singleShot(0, pushBtn, [pushBtn]() {
    qInfo()
        << "TestServer::clickDialogButton - Executing deferred button click";
    pushBtn->click();
    qInfo() << "TestServer::clickDialogButton - Deferred click completed";
  });

  qInfo() << "TestServer::clickDialogButton - Deferred click scheduled, "
             "returning true";
  return true;
}

bool TestServer::triggerMenuAction(const QString &menuName,
                                   const QString &actionName) {
  QMenuBar *menuBar = m_window->menuBar();
  if (!menuBar)
    return false;

  for (QAction *menuAction : menuBar->actions()) {
    if (menuAction->text() == menuName) {
      QMenu *menu = menuAction->menu();
      if (!menu)
        continue;

      for (QAction *action : menu->actions()) {
        // simple contains check to handle "&Create Session..." etc
        if (action->text().contains(actionName)) {
          qInfo() << "TestServer: Triggering action" << action->text();
          action->trigger();
          return true;
        }
      }
    }
  }
  qWarning() << "TestServer: Menu action not found:" << menuName << "->"
             << actionName;
  return false;
}

QJsonObject TestServer::executeCommand(const QJsonObject &cmd) {
  QString action = cmd["action"].toString();
  QJsonObject args = cmd["args"].toObject();
  QJsonObject result;
  result["cmd"] = action;

  bool success = false;

  if (action == "click") {
    success = clickWidget(args["widget"].toString());
  } else if (action == "type") {
    success = typeText(args["widget"].toString(), args["text"].toString());
  } else if (action == "press_key") {
    success = pressKey(args["widget"].toString(), args["key"].toString());
  } else if (action == "click_dialog_button") {
    success =
        clickDialogButton(args["widget"].toString(), args["button"].toString());
  } else if (action == "trigger_menu") {
    success =
        triggerMenuAction(args["menu"].toString(), args["action"].toString());
  } else if (action == "ping") {
    success = true;
  } else {
    result["error"] = "Unknown command";
  }

  result["success"] = success;
  return result;
}
