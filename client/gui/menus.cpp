#include "menus.h"
#include "client.pb.h"
#include "gui/app.h"
#include "gui/right_panel.h"
#include "p2p/session.pb.h"
#include "roles/permissions.h"
#include "transport/control_stream_worker.h"

#include <QAction>
#include <QApplication>
#include <QCheckBox>
#include <QClipboard>
#include <QComboBox>
#include <QDebug>
#include <QDialog>
#include <QDialogButtonBox>
#include <QDir>
#include <QFileDialog>
#include <QFormLayout>
#include <QGroupBox>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QLineEdit>
#include <QMenuBar>
#include <QMessageBox>
#include <QPushButton>
#include <QSpinBox>
#include <QTabWidget>
#include <QTableWidget>
#include <QVBoxLayout>

namespace gui {

std::function<void()> SidebarToggleCallback;

// Helper function moved here or to a common utility. For now, static in this
// TU.
static void showSessionInfoDialogHelper(
    const QString &title, const QString &messageBody, const QString &sessionId,
    const QString &sessionName, const QString &inviteCode, QWidget *parent) {
  QDialog infoDialog(parent);
  infoDialog.setWindowTitle(title);
  infoDialog.setObjectName("sessionInfoDialog");
  QVBoxLayout *layout = new QVBoxLayout(&infoDialog);

  layout->addWidget(new QLabel(messageBody, &infoDialog));
  if (!sessionId.isEmpty()) {
    layout->addWidget(
        new QLabel(QString("Session ID: %1").arg(sessionId), &infoDialog));
  }
  if (!sessionName.isEmpty()) {
    layout->addWidget(
        new QLabel(QString("Session Name: %1").arg(sessionName), &infoDialog));
  }

  if (!inviteCode.isEmpty()) {
    QHBoxLayout *inviteLayout = new QHBoxLayout();
    QLabel *inviteLabel =
        new QLabel(QString("Invite Code: %1").arg(inviteCode), &infoDialog);
    inviteLabel->setObjectName("inviteCodeLabel");
    inviteLayout->addWidget(inviteLabel);
    QPushButton *copyButton = new QPushButton("⧉");
    copyButton->setToolTip("Copy Invite Code to Clipboard");
    copyButton->setFixedSize(copyButton->fontMetrics().height() * 2,
                             copyButton->fontMetrics().height() * 2);
    QObject::connect(copyButton, &QPushButton::clicked, [inviteCode]() {
      QApplication::clipboard()->setText(inviteCode);
    });
    inviteLayout->addWidget(copyButton);
    inviteLayout->addStretch();
    layout->addLayout(inviteLayout);
  }
  QDialogButtonBox *buttonBox =
      new QDialogButtonBox(QDialogButtonBox::Ok, &infoDialog);
  buttonBox->setObjectName("sessionInfoBtnBox");
  QObject::connect(buttonBox, &QDialogButtonBox::accepted, &infoDialog,
                   &QDialog::accept);
  layout->addWidget(buttonBox);
  qInfo() << "showSessionInfoDialogHelper: About to exec() dialog with title:"
          << title;
  infoDialog.exec();
  qInfo() << "showSessionInfoDialogHelper: Dialog dismissed.";
}

static void openManageRoleDefinitionsDialog(QMainWindow *window,
                                            P2P::Roles::RoleStore *roleStore,
                                            P2P::ControlStreamWorker *worker) {
  QDialog manageDialog(window);
  manageDialog.setWindowTitle("Manage Role Definitions");
  QVBoxLayout *layout = new QVBoxLayout(&manageDialog);

  QTableWidget *rolesTable = new QTableWidget();
  rolesTable->setColumnCount(2);
  rolesTable->setHorizontalHeaderLabels({"Role Name", "Permissions"});
  rolesTable->horizontalHeader()->setStretchLastSection(true);
  rolesTable->setEditTriggers(QAbstractItemView::NoEditTriggers);
  rolesTable->setSelectionBehavior(QAbstractItemView::SelectRows);
  rolesTable->setSelectionMode(QAbstractItemView::SingleSelection);
  layout->addWidget(rolesTable);

  QHBoxLayout *buttonLayout = new QHBoxLayout();
  QPushButton *addButton = new QPushButton("Add...");
  QPushButton *editButton = new QPushButton("Edit...");
  QPushButton *removeButton = new QPushButton("Remove");
  buttonLayout->addWidget(addButton);
  buttonLayout->addWidget(editButton);
  buttonLayout->addWidget(removeButton);
  layout->addLayout(buttonLayout);

  const quint32 myPerms =
      roleStore->getPermissionsForPeer(roleStore->getLocalPeerId());

  const auto permissionMap = P2P::Roles::getPermissionDescriptions();

  auto openRoleEditor = [&](const client::p2p::RoleDefinition *existingRole =
                                nullptr) {
    QDialog editor(&manageDialog);
    editor.setWindowTitle(existingRole ? "Edit Role" : "Add Role");
    QFormLayout *form = new QFormLayout(&editor);

    QLineEdit *nameInput = new QLineEdit(
        existingRole ? QString::fromStdString(existingRole->name()) : "");
    form->addRow("Role Name:", nameInput);

    QGroupBox *permsBox = new QGroupBox("Permissions", &editor);
    QGridLayout *permsLayout = new QGridLayout();
    permsBox->setLayout(permsLayout);

    QMap<P2P::Roles::Permission, QCheckBox *> checkBoxes;
    int row = 0, col = 0;
    for (auto it = permissionMap.constBegin(); it != permissionMap.constEnd();
         ++it) {
      QCheckBox *cb = new QCheckBox(it.value());
      if (existingRole &&
          P2P::Roles::hasPermission(existingRole->permissions(), it.key())) {
        cb->setChecked(true);
      }
      // User can only grant permissions they themselves possess.
      if (!P2P::Roles::hasPermission(myPerms, it.key())) {
        cb->setDisabled(true);
        cb->setToolTip(
            "You do not have this permission, so you cannot grant it.");
      }
      checkBoxes[it.key()] = cb;
      permsLayout->addWidget(cb, row, col);
      if (++col % 2 == 0) {
        row++;
        col = 0;
      }
    }
    form->addRow(permsBox);

    QDialogButtonBox *buttonBox = new QDialogButtonBox(
        QDialogButtonBox::Ok | QDialogButtonBox::Cancel, &editor);
    form->addRow(buttonBox);

    QObject::connect(buttonBox, &QDialogButtonBox::accepted, &editor,
                     &QDialog::accept);
    QObject::connect(buttonBox, &QDialogButtonBox::rejected, &editor,
                     &QDialog::reject);

    if (editor.exec() == QDialog::Accepted && !nameInput->text().isEmpty()) {
      quint32 perms = 0;
      for (auto it = checkBoxes.constBegin(); it != checkBoxes.constEnd();
           ++it) {
        if (it.value()->isChecked()) {
          perms |= it.key();
        }
      }
      client::ClientMsg msg;
      auto *cmd = msg.mutable_update_role_cmd();
      cmd->mutable_definition()->set_name(nameInput->text().toStdString());
      cmd->mutable_definition()->set_permissions(perms);
      cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());
      worker->send(msg);
    }
  };

  auto populateTable = [&]() {
    rolesTable->clearContents();
    QMap<QString, client::p2p::RoleDefinition> defs =
        roleStore->getRoleDefinitions();
    rolesTable->setRowCount(defs.size());
    int row = 0;
    for (const auto &def : defs) {
      QString permsStr;
      for (auto it = permissionMap.constBegin(); it != permissionMap.constEnd();
           ++it) {
        if (P2P::Roles::hasPermission(def.permissions(), it.key())) {
          if (!permsStr.isEmpty())
            permsStr += ", ";
          permsStr += it.value();
        }
      }
      rolesTable->setItem(
          row, 0, new QTableWidgetItem(QString::fromStdString(def.name())));
      rolesTable->setItem(row, 1, new QTableWidgetItem(permsStr));
      rolesTable->item(row, 0)->setData(Qt::UserRole, QVariant::fromValue(def));
      row++;
    }
  };

  populateTable();
  QObject::connect(roleStore, &P2P::Roles::RoleStore::definitionsChanged,
                   &manageDialog, populateTable);

  QObject::connect(addButton, &QPushButton::clicked, &manageDialog,
                   [&]() { openRoleEditor(); });

  QObject::connect(editButton, &QPushButton::clicked, &manageDialog, [&]() {
    if (rolesTable->currentRow() < 0)
      return;
    QTableWidgetItem *item = rolesTable->item(rolesTable->currentRow(), 0);
    client::p2p::RoleDefinition existingRole =
        item->data(Qt::UserRole).value<client::p2p::RoleDefinition>();
    openRoleEditor(&existingRole);
  });

  QObject::connect(removeButton, &QPushButton::clicked, &manageDialog, [&]() {
    if (rolesTable->currentRow() < 0)
      return;
    QTableWidgetItem *item = rolesTable->item(rolesTable->currentRow(), 0);
    QString roleName = item->text();
    if (QMessageBox::question(
            &manageDialog, "Confirm Removal",
            QString("Are you sure you want to remove the role '%1'?")
                .arg(roleName),
            QMessageBox::Yes | QMessageBox::No) == QMessageBox::Yes) {
      client::ClientMsg msg;
      auto *cmd = msg.mutable_remove_role_cmd();
      cmd->set_role_name(roleName.toStdString());
      cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());
      worker->send(msg);
    }
  });

  QDialogButtonBox *closeButton = new QDialogButtonBox(QDialogButtonBox::Close);
  layout->addWidget(closeButton);
  QObject::connect(closeButton, &QDialogButtonBox::rejected, &manageDialog,
                   &QDialog::reject);

  manageDialog.exec();
}

// Helper function to rebuild the roles submenu
// This needs access to rolesMenu, roleStore, worker, and myPeerIdStd
static void rebuildRolesMenu(QMenu *rolesMenu, P2P::Roles::RoleStore *roleStore,
                             P2P::ControlStreamWorker *worker,
                             QMainWindow *window) {
  // Get the local peer ID from RoleStore
  QString myPeerIdQString = roleStore->getLocalPeerId();
  // If it's not set yet (e.g., initial call before LocalPeerIdentity
  // received), we might pass a temporary/dummy or disable certain actions.
  // For now, proceed if empty.

  if (!rolesMenu || !roleStore || !worker)
    return;

  qDebug() << "rebuildRolesMenu: Rebuilding for peer:" << myPeerIdQString;

  rolesMenu->clear(); // Clear existing role actions

  QMap<QString, client::p2p::RoleDefinition> definitions =
      roleStore->getRoleDefinitions();
  // QVector<QString> currentMyRoles;
  QVector<QString> currentMyRolesLower;
  if (!myPeerIdQString.isEmpty()) {
    // currentMyRoles = roleStore->getAssignedRoleNames(myPeerIdQString);
    for (const auto &r : roleStore->getAssignedRoleNames(myPeerIdQString))
      currentMyRolesLower.push_back(r.toLower());
  }

  qDebug() << "rebuildRolesMenu: Definitions count:" << definitions.size()
           << "My current roles:" << currentMyRolesLower;

  // Who may toggle?
  const bool canManageOwnRoles =
      !myPeerIdQString.isEmpty() &&
      P2P::Roles::hasPermission(
          roleStore->getPermissionsForPeer(myPeerIdQString),
          P2P::Roles::PermManageUserRoles);

  // Lambda sent with every QAction (only fires if the action is enabled)
  auto onRoleActionToggled = [rolesMenu, worker, myPeerIdQString, roleStore]() {
    if (myPeerIdQString.isEmpty()) {
      qWarning() << "GUI: Cannot set roles, local peer ID not yet known.";
      return;
    }
    client::ClientMsg clientMsg;
    client::p2p::SetPeerRolesCmd *cmd = clientMsg.mutable_set_peer_roles_cmd();
    cmd->set_target_peer_id(myPeerIdQString.toStdString());

    QList<QAction *> allMenuActions = rolesMenu->actions();
    QVector<QString> newDesiredRoles;

    for (QAction *action : allMenuActions) {
      if (action->property("isRoleAction").toBool() && action->isChecked()) {
        const QString key = action->property("roleKey").toString();
        newDesiredRoles.append(key);
        cmd->add_assigned_role_names(key.toStdString());
      }
    }

    // Ensure "Viewer" is always present if it's a defined role and no other
    // roles grant view. This is complex client-side logic; ideally, the
    // server enforces baseline roles or the client simply sends its desired
    // set and the server adjusts. For now, we send what the user checked.
    // If newDesiredRoles is empty and "Viewer" exists, we might want to
    // default to "Viewer". For simplicity here: if nothing is checked, an
    // empty role list is sent. The server can decide.

    // cmd->set_hlc_ts(...); // Client might set its HLC
    qDebug() << "GUI: Requesting role change for self. New roles set:"
             << newDesiredRoles;
    worker->send(clientMsg);
  };

  for (auto it = definitions.constBegin(); it != definitions.constEnd(); ++it) {
    const QString roleKey = it.key();
    const auto &definition = it.value();
    const QString displayName = QString::fromStdString(definition.name());

    QAction *roleAction = rolesMenu->addAction(displayName);
    roleAction->setCheckable(true);
    roleAction->setProperty("isRoleAction", true);
    roleAction->setProperty("roleKey", roleKey);
    roleAction->setEnabled(canManageOwnRoles);

    qDebug() << "rebuildRolesMenu: Creating action for role:" << displayName
             << "IsCheckable:" << roleAction->isCheckable()
             << "IsRoleAction:" << roleAction->property("isRoleAction")
             << "RoleKey:" << roleAction->property("roleKey");

    // Check if the user currently has this role
    if (currentMyRolesLower.contains(roleKey)) {
      roleAction->setChecked(true);
      qDebug() << "rebuildRolesMenu: Role" << displayName << "is CHECKED";
    }

    // Connect to toggled() signal, which is better for checkable actions
    // than triggered(), as it fires *after* the check state has changed.
    if (canManageOwnRoles)
      QObject::connect(roleAction, &QAction::toggled, window,
                       onRoleActionToggled);
  }

  rolesMenu->addSeparator();
  QAction *manageDefinitionsAction =
      rolesMenu->addAction("Manage Role Definitions...");
  const bool canManageDefs =
      !myPeerIdQString.isEmpty() &&
      P2P::Roles::hasPermission(
          roleStore->getPermissionsForPeer(myPeerIdQString),
          P2P::Roles::PermAddRemoveRoles);

  manageDefinitionsAction->setEnabled(canManageDefs);
  QObject::connect(manageDefinitionsAction, &QAction::triggered, window, [=]() {
    openManageRoleDefinitionsDialog(window, roleStore, worker);
  });
}

void createMenus(QMainWindow *window, gui::App *app,
                 P2P::Roles::RoleStore *roleStore, QSplitter *mainSplitter,
                 QWidget *rightPanel, QTabWidget *rightPanelTabs,
                 QWidget *leftPanel, P2P::ControlStreamWorker *worker) {
  QMenuBar *menuBar = window->menuBar();

  // Session menu
  QMenu *sessionMenu = menuBar->addMenu("Session");

  // --- Create Session Action ---
  QAction *createSessionAction = sessionMenu->addAction("Create Session...");
  QObject::connect(
      createSessionAction, &QAction::triggered, [window, worker, app]() {
        if (!worker) {
          QMessageBox::critical(window, "Error",
                                "Not connected to Peer Service.");
          return;
        }
        if (app && !app->getCurrentSessionId().isEmpty()) {
          QMessageBox::information(window, "Session Active",
                                   "You are already in a session. Please "
                                   "leave it to create a new one.");
          return;
        }

        QDialog createDialog(window);
        createDialog.setWindowTitle("Create New Session");
        QFormLayout *form = new QFormLayout(&createDialog);

        QLineEdit *sessionNameInput = new QLineEdit(&createDialog);
        sessionNameInput->setObjectName("sessionNameInput");
        sessionNameInput->setPlaceholderText(
            "(Optional) My Awesome Watch Party");
        form->addRow("Session Name:", sessionNameInput);

        QLineEdit *usernameInput = new QLineEdit(&createDialog);
        usernameInput->setObjectName("usernameInput");
        // TODO: Pre-fill with a saved username preference if available
        usernameInput->setPlaceholderText("Your Display Name");
        form->addRow("Your Username:", usernameInput);

        QDialogButtonBox *buttonBox = new QDialogButtonBox(
            QDialogButtonBox::Ok | QDialogButtonBox::Cancel, &createDialog);
        buttonBox->setObjectName("createSessionBtnBox");
        form->addRow(buttonBox);

        QObject::connect(buttonBox, &QDialogButtonBox::accepted, &createDialog,
                         &QDialog::accept);
        QObject::connect(buttonBox, &QDialogButtonBox::rejected, &createDialog,
                         &QDialog::reject);

        qInfo() << "GUI: CreateSession dialog exec() about to be called...";
        int dialogResult = createDialog.exec();
        qInfo() << "GUI: CreateSession dialog exec() returned:" << dialogResult
                << "(Accepted=" << QDialog::Accepted << ")";

        if (dialogResult == QDialog::Accepted) {
          qInfo()
              << "GUI: CreateSession dialog ACCEPTED. Reading input values...";
          QString requestedSessionName = sessionNameInput->text();
          QString requestedUsername = usernameInput->text();
          qInfo() << "GUI: Read values - sessionName:" << requestedSessionName
                  << "username:" << requestedUsername;

          // Disconnect previous connections if any to avoid multiple
          // dialogs It's safer to use unique connections or ensure App
          // handles one-shot signals. For this, we'll assume App's
          // signals are emitted once per response.
          qInfo() << "GUI: Setting up signal connections...";
          QObject::connect(
              app, &gui::App::sessionCreatedSuccessfully, window,
              [requestedSessionName, window](const QString &sid,
                                             const QString &invCode) {
                qInfo() << "GUI: sessionCreatedSuccessfully signal received!";
                showSessionInfoDialogHelper( // Call static helper in
                                             // menus.cpp
                    "Session Created", "Session successfully created!", sid,
                    requestedSessionName, invCode, window);
              },
              Qt::SingleShotConnection); // Ensure it only fires once

          QObject::connect(
              app, &gui::App::sessionCreationFailed, window,
              [window](const QString &errorMsg) {
                qInfo() << "GUI: sessionCreationFailed signal received!";
                QMessageBox::critical(window, "Session Creation Failed",
                                      errorMsg);
              },
              Qt::SingleShotConnection); // Ensure it only fires once
          qInfo() << "GUI: Signal connections established.";

          client::ClientMsg clientMsg;
          // Use the correct generated type:
          // client::p2p::CreateSessionRequest
          client::p2p::CreateSessionRequest *req =
              clientMsg.mutable_create_session_request();

          QString sessionName = requestedSessionName;
          QString username = requestedUsername;

          if (!sessionName.isEmpty()) {
            req->set_session_name(sessionName.toStdString());
          }
          if (!username.isEmpty()) {
            req->set_username(username.toStdString());
          }
          qInfo() << "GUI: About to call worker->send() for "
                     "CreateSessionRequest...";
          worker->send(clientMsg);
          qInfo() << "GUI: worker->send() completed for CreateSessionRequest.";
          // Response handled in App::onServerMessage
        } else {
          qInfo() << "GUI: CreateSession dialog was cancelled or rejected.";
        }
        qInfo() << "GUI: CreateSession action handler completing.";
      });

  // --- Join Session Action ---
  QAction *joinSessionAction = sessionMenu->addAction("Join Session...");
  QObject::connect(
      joinSessionAction, &QAction::triggered, [window, worker, app]() {
        if (!worker) {
          QMessageBox::critical(window, "Error",
                                "Not connected to Peer Service.");
          return;
        }
        if (app && !app->getCurrentSessionId().isEmpty()) {
          QMessageBox::information(window, "Session Active",
                                   "You are already in a session. Please "
                                   "leave it to join another.");
          return;
        }

        QDialog joinDialog(window);
        joinDialog.setWindowTitle("Join Existing Session");
        QFormLayout *form = new QFormLayout(&joinDialog);

        QLineEdit *inviteCodeInput = new QLineEdit(&joinDialog);
        inviteCodeInput->setObjectName("inviteCodeInput");
        inviteCodeInput->setPlaceholderText("Enter Invite Code (Session ID)");
        form->addRow("Invite Code:", inviteCodeInput);

        QLineEdit *usernameInput = new QLineEdit(&joinDialog);
        usernameInput->setObjectName("joinUsernameInput");
        usernameInput->setPlaceholderText("Your Display Name");
        form->addRow("Your Username:", usernameInput);

        QDialogButtonBox *buttonBox = new QDialogButtonBox(
            QDialogButtonBox::Ok | QDialogButtonBox::Cancel, &joinDialog);
        buttonBox->setObjectName("joinSessionBtnBox");
        form->addRow(buttonBox);

        QObject::connect(buttonBox, &QDialogButtonBox::accepted, &joinDialog,
                         &QDialog::accept);
        QObject::connect(buttonBox, &QDialogButtonBox::rejected, &joinDialog,
                         &QDialog::reject);

        if (joinDialog.exec() == QDialog::Accepted) {
          QString inviteCode = inviteCodeInput->text().trimmed();
          QString username = usernameInput->text();

          if (inviteCode.isEmpty()) {
            QMessageBox::warning(&joinDialog, "Input Error",
                                 "Invite Code cannot be empty.");
            return;
          }

          client::ClientMsg clientMsg;
          // Use the correct generated type:
          // client::p2p::JoinSessionRequest
          client::p2p::JoinSessionRequest *req =
              clientMsg.mutable_join_session_request();
          req->set_invite_code(inviteCode.toStdString());
          if (!username.isEmpty()) {
            req->set_username(username.toStdString());
          }
          qDebug() << "GUI: Sending JoinSessionRequest. Code:" << inviteCode
                   << "User:" << username;
          worker->send(clientMsg);
          // Response handled in App::onServerMessage
        }
      });

  // --- Copy Invite Code Action ---
  QAction *copyInviteAction = sessionMenu->addAction("Copy Invite Code");
  copyInviteAction->setEnabled(false); // Initially disabled
  QObject::connect(
      copyInviteAction, &QAction::triggered,
      [app, window]() { // Added window for potential QMessageBox parent
        if (!app)
          return;
        QString inviteCode =
            app->getCurrentInviteCode(); // Assumes App stores this
        if (!inviteCode.isEmpty()) {
          QApplication::clipboard()->setText(inviteCode);
          QMessageBox::information(window, "Invite Code Copied",
                                   "Invite code copied to clipboard!");
          qInfo() << "Invite code copied to clipboard:" << inviteCode;
        } else {
          QMessageBox::warning(
              window, "No Invite Code",
              "No active session or invite code available to copy.");
          qWarning() << "Copy Invite Code: No active session or invite "
                        "code available.";
        }
      });

  // TODO: Add "Leave Session" action here. It should:
  // - Notify the Peer Service (new ClientMsg type needed e.g.,
  // LeaveSessionRequest)
  // - Peer Service handles P2P cleanup for leaving.
  // - App calls clearActiveSessionDetails() and updates UI.
  QAction *leaveAction = sessionMenu->addAction("Leave Session");
  leaveAction->setEnabled(false); // Enable when in a session
  // QObject::connect(leaveAction, &QAction::triggered, ...);

  sessionMenu->addSeparator();
  QAction *quitAction = sessionMenu->addAction("Quit");
  QObject::connect(quitAction, &QAction::triggered, window,
                   &QMainWindow::close);

  // Function to update enabled state of session actions
  auto updateSessionActions = [app, roleStore, createSessionAction,
                               joinSessionAction, copyInviteAction,
                               leaveAction]() {
    if (!app)
      return;
    bool sessionActive = !app->getCurrentSessionId().isEmpty();
    createSessionAction->setEnabled(!sessionActive);
    joinSessionAction->setEnabled(!sessionActive);
    leaveAction->setEnabled(sessionActive);

    if (sessionActive && roleStore) {
      quint32 perms =
          roleStore->getPermissionsForPeer(roleStore->getLocalPeerId());
      copyInviteAction->setEnabled(
          P2P::Roles::hasPermission(perms, P2P::Roles::PermInvite));
    } else {
      copyInviteAction->setEnabled(false);
    }
  };

  // Initial state update
  updateSessionActions();

  // Connect signals for dynamic updates
  // This requires App to emit a signal when its session state changes,
  // or we connect to individual RoleStore signals as before.
  // For simplicity, let's assume App will have a sessionStateChanged signal
  // eventually. For now, connect to RoleStore signals that imply a possible
  // change in permissions or session context.
  if (app) { // Connect to App's signal if app is valid
    QObject::connect(app, &gui::App::sessionStateChanged, window,
                     updateSessionActions);
  }
  if (roleStore) {
    QObject::connect(
        roleStore, &P2P::Roles::RoleStore::peerRolesChanged, window,
        [updateSessionActions, roleStore, app](const QString &peerId) {
          if (app && roleStore && peerId == roleStore->getLocalPeerId())
            updateSessionActions();
        });
    QObject::connect(
        roleStore, &P2P::Roles::RoleStore::localPeerIdConfirmed, window,
        updateSessionActions); // When our ID is known, perms can be checked
    QObject::connect(
        roleStore, &P2P::Roles::RoleStore::allAssignmentsRefreshed, window,
        updateSessionActions); // Full refresh might change our roles
  }
  // A more direct signal from App would be:
  // QObject::connect(app, &gui::App::sessionStatusChanged, window,
  // updateSessionActions); This signal would be emitted by App after
  // setActiveSessionDetails or clearActiveSessionDetails.

  // Settings menu
  QMenu *settingsMenu = menuBar->addMenu("Settings");
  QAction *prefsAction = settingsMenu->addAction("Preferences");
  QObject::connect(prefsAction, &QAction::triggered, [window]() {
    QDialog prefsDialog(window);
    prefsDialog.setWindowTitle("Preferences");
    QTabWidget *tabs = new QTabWidget(&prefsDialog);

    // General Tab
    QWidget *generalTab = new QWidget();
    QFormLayout *generalLayout = new QFormLayout(generalTab);
    QLineEdit *usernameInput = new QLineEdit();
    usernameInput->setText("User123");
    generalLayout->addRow("Username:", usernameInput);
    QLineEdit *savePathInput = new QLineEdit();
    savePathInput->setText(QDir::tempPath());
    generalLayout->addRow("Download Path:", savePathInput);

    // Network Tab
    QWidget *networkTab = new QWidget();
    QFormLayout *networkLayout = new QFormLayout(networkTab);
    QSpinBox *portInput = new QSpinBox();
    portInput->setRange(1024, 65535);
    portInput->setValue(8080);
    networkLayout->addRow("Port:", portInput);
    QComboBox *bandwidthLimit = new QComboBox();
    bandwidthLimit->addItems({"No limit", "2 Mbps", "5 Mbps", "10 Mbps"});
    networkLayout->addRow("Bandwidth Limit:", bandwidthLimit);

    tabs->addTab(generalTab, "General");
    tabs->addTab(networkTab, "Network");

    QVBoxLayout *dialogLayout = new QVBoxLayout(&prefsDialog);
    dialogLayout->addWidget(tabs);
    QDialogButtonBox *buttonBox =
        new QDialogButtonBox(QDialogButtonBox::Ok | QDialogButtonBox::Cancel);
    dialogLayout->addWidget(buttonBox);
    QObject::connect(buttonBox, &QDialogButtonBox::accepted, &prefsDialog,
                     &QDialog::accept);
    QObject::connect(buttonBox, &QDialogButtonBox::rejected, &prefsDialog,
                     &QDialog::reject);
    prefsDialog.resize(400, 300);
    prefsDialog.exec();
  });

  // Peers menu
  QMenu *peersMenu = menuBar->addMenu("Peers");
  QAction *managePeersAction = peersMenu->addAction("Show Peers Panel");
  QObject::connect(managePeersAction, &QAction::triggered,
                   [window, rightPanel, mainSplitter, rightPanelTabs]() {
                     if (rightPanel && !rightPanel->isVisible()) {
                       rightPanel->show();
                       QList<int> sizes;
                       sizes << int(window->width() * 0.65)
                             << int(window->width() * 0.35);
                       mainSplitter->setSizes(sizes);
                     }
                     if (rightPanelTabs) {
                       for (int i = 0; i < rightPanelTabs->count(); ++i) {
                         if (rightPanelTabs->tabText(i) == "Peers") {
                           rightPanelTabs->setCurrentIndex(i);
                           break;
                         }
                       }
                     }
                   });

  // View menu
  QMenu *viewMenu = menuBar->addMenu("View");
  QAction *toggleSidebarAction = viewMenu->addAction("Toggle Sidebar");
  SidebarToggleCallback = [window, mainSplitter, rightPanel]() {
    if (rightPanel->isVisible()) {
      rightPanel->hide();
      QList<int> sizes;
      sizes << window->width() << 0;
      mainSplitter->setSizes(sizes);
    } else {
      rightPanel->show();
      QList<int> sizes;
      sizes << int(window->width() * 0.65) << int(window->width() * 0.35);
      mainSplitter->setSizes(sizes);
    }
  };
  QObject::connect(toggleSidebarAction, &QAction::triggered, []() {
    if (SidebarToggleCallback)
      SidebarToggleCallback();
  });

  // Roles menu
  QMenu *rolesMenu = menuBar->addMenu("Roles");

  // For demonstration, we'll assume changing "own" roles is done via a
  // command to the server. The actual `P2P::Peer* peer` object on the client
  // is a dummy and its roles field isn't authoritative. The peer->peerId
  // would be needed to identify "self" to the server. This part requires
  // ControlStreamWorker to be passed to createMenus if commands are sent from
  // here.

  // Placeholder for local peer ID - this needs to be the actual ID known by
  // the service

  // Initial population
  rebuildRolesMenu(rolesMenu, roleStore, worker, window);
  // rebuildRolesMenu(rolesMenu, roleStore, worker, "" /* dummy, rebuild uses
  // roleStore->getLocalPeerId() */, window);

  // Connect to RoleStore signals to rebuild the menu when definitions or my
  // assignments change
  QObject::connect(roleStore, &P2P::Roles::RoleStore::definitionsChanged,
                   window, [rolesMenu, roleStore, worker, window]() {
                     qDebug() << "MENUS: definitionsChanged signal received. "
                                 "Rebuilding roles menu for local peer ("
                              << roleStore->getLocalPeerId() << ").";
                     rebuildRolesMenu(rolesMenu, roleStore, worker, window);
                   });

  QObject::connect(
      roleStore, &P2P::Roles::RoleStore::peerRolesChanged, window,
      [rolesMenu, roleStore, worker, window](const QString &changedPeerId) {
        QString localPeerId = roleStore->getLocalPeerId();
        if (localPeerId == changedPeerId && !localPeerId.isEmpty()) {
          qDebug() << "MENUS: peerRolesChanged signal received for myPeerId ("
                   << changedPeerId << "). Rebuilding roles menu.";
          rebuildRolesMenu(rolesMenu, roleStore, worker, window);
        }
      });

  QObject::connect(roleStore, &P2P::Roles::RoleStore::allAssignmentsRefreshed,
                   window, [rolesMenu, roleStore, worker, window]() {
                     qDebug()
                         << "MENUS: allAssignmentsRefreshed signal received. "
                            "Rebuilding roles menu for local peer ("
                         << roleStore->getLocalPeerId() << ").";
                     rebuildRolesMenu(rolesMenu, roleStore, worker, window);
                   });

  QObject::connect(
      roleStore, &P2P::Roles::RoleStore::localPeerIdConfirmed, window,
      [rolesMenu, roleStore, worker, window](const QString & /*localId*/) {
        qDebug() << "MENUS: localPeerIdConfirmed signal received. "
                    "Rebuilding roles menu for local peer ("
                 << roleStore->getLocalPeerId() << ").";
        rebuildRolesMenu(rolesMenu, roleStore, worker, window);
      });

  // Help menu
  QMenu *helpMenu = menuBar->addMenu("Help");
  QAction *aboutAction = helpMenu->addAction("About");
  QObject::connect(aboutAction, &QAction::triggered, [window]() {
    QMessageBox::information(
        window, "About",
        "P2PTogether v0.1\n\nA synchronized video watching platform");
  });
}

} // namespace gui
