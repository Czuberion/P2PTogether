#include "menus.h"
#include "gui/right_panel.h"
#include "roles/permissions.h"
#include <QAction>
#include <QComboBox>
#include <QDebug>
#include <QDialog>
#include <QDialogButtonBox>
#include <QDir>
#include <QFileDialog>
#include <QFormLayout>
#include <QHBoxLayout>
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

// Helper function to rebuild the roles submenu
// This needs access to rolesMenu, roleStore, worker, and myPeerIdStd
static void rebuildRolesMenu(QMenu* rolesMenu, P2P::Roles::RoleStore* roleStore,
                             P2P::ControlStreamWorker* worker,
                             QMainWindow* window) {
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
        for (const auto& r : roleStore->getAssignedRoleNames(myPeerIdQString))
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
    auto onRoleActionToggled = [rolesMenu, worker, myPeerIdQString,
                                roleStore]() {
        if (myPeerIdQString.isEmpty()) {
            qWarning() << "GUI: Cannot set roles, local peer ID not yet known.";
            return;
        }
        client::ClientMsg clientMsg;
        client::p2p::SetPeerRolesCmd* cmd =
            clientMsg.mutable_set_peer_roles_cmd();
        cmd->set_target_peer_id(myPeerIdQString.toStdString());

        QList<QAction*> allMenuActions = rolesMenu->actions();
        QVector<QString> newDesiredRoles;

        for (QAction* action : allMenuActions) {
            if (action->property("isRoleAction").toBool() &&
                action->isChecked()) {
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

    for (auto it = definitions.constBegin(); it != definitions.constEnd();
         ++it) {
        const QString roleKey     = it.key();
        const auto& definition    = it.value();
        const QString displayName = QString::fromStdString(definition.name());

        QAction* roleAction = rolesMenu->addAction(displayName);
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
    QAction* manageDefinitionsAction =
        rolesMenu->addAction("Manage Role Definitions...");
    const bool canManageDefs =
        !myPeerIdQString.isEmpty() &&
        P2P::Roles::hasPermission(
            roleStore->getPermissionsForPeer(myPeerIdQString),
            P2P::Roles::PermAddRemoveRoles);
    manageDefinitionsAction->setEnabled(canManageDefs);
    // TODO: Connect manageDefinitionsAction to a dialog
    // TODO: Update manageDefinitionsAction->setEnabled state when local peer's
    // permissions change
}

void createMenus(QMainWindow* window, P2P::Peer* peer,
                 P2P::Roles::RoleStore* roleStore, QSplitter* mainSplitter,
                 QWidget* rightPanel, QWidget* leftPanel,
                 P2P::ControlStreamWorker* worker) {
    QMenuBar* menuBar = window->menuBar();

    // Session menu
    QMenu* sessionMenu  = menuBar->addMenu("Session");
    QAction* joinAction = sessionMenu->addAction("Join Session");
    QObject::connect(joinAction, &QAction::triggered, [window]() {
        QDialog dialog(window);
        dialog.setWindowTitle("Join Session");
        QVBoxLayout* dialogLayout = new QVBoxLayout(&dialog);
        dialogLayout->addWidget(
            new QLabel("Enter a session ID to join:", &dialog));
        QLineEdit* sessionInput = new QLineEdit(&dialog);
        sessionInput->setPlaceholderText("Session ID");
        dialogLayout->addWidget(sessionInput);
        QDialogButtonBox* buttonBox = new QDialogButtonBox(
            QDialogButtonBox::Ok | QDialogButtonBox::Cancel, &dialog);
        dialogLayout->addWidget(buttonBox);
        QObject::connect(buttonBox, &QDialogButtonBox::accepted, &dialog,
                         &QDialog::accept);
        QObject::connect(buttonBox, &QDialogButtonBox::rejected, &dialog,
                         &QDialog::reject);
        if (dialog.exec() == QDialog::Accepted) {
            QString sessionID = sessionInput->text();
            if (!sessionID.isEmpty()) {
                QMessageBox::information(
                    window, "Session Joined",
                    QString("You've joined session: %1").arg(sessionID));
            }
        }
    });

    QAction* leaveAction = sessionMenu->addAction("Leave Session");
    QObject::connect(leaveAction, &QAction::triggered, [window]() {
        QMessageBox::information(window, "Leave", "Left current session");
    });
    sessionMenu->addSeparator();
    QAction* quitAction = sessionMenu->addAction("Quit");
    QObject::connect(quitAction, &QAction::triggered, [peer, window]() {
        // peer->cleanup(); // cleanup is now part of QApplication::aboutToQuit
        window->close();
    });

    // Settings menu
    QMenu* settingsMenu  = menuBar->addMenu("Settings");
    QAction* prefsAction = settingsMenu->addAction("Preferences");
    QObject::connect(prefsAction, &QAction::triggered, [window]() {
        QDialog prefsDialog(window);
        prefsDialog.setWindowTitle("Preferences");
        QTabWidget* tabs = new QTabWidget(&prefsDialog);

        // General Tab
        QWidget* generalTab        = new QWidget();
        QFormLayout* generalLayout = new QFormLayout(generalTab);
        QLineEdit* usernameInput   = new QLineEdit();
        usernameInput->setText("User123");
        generalLayout->addRow("Username:", usernameInput);
        QLineEdit* savePathInput = new QLineEdit();
        savePathInput->setText(QDir::tempPath());
        generalLayout->addRow("Download Path:", savePathInput);

        // Network Tab
        QWidget* networkTab        = new QWidget();
        QFormLayout* networkLayout = new QFormLayout(networkTab);
        QSpinBox* portInput        = new QSpinBox();
        portInput->setRange(1024, 65535);
        portInput->setValue(8080);
        networkLayout->addRow("Port:", portInput);
        QComboBox* bandwidthLimit = new QComboBox();
        bandwidthLimit->addItems({"No limit", "2 Mbps", "5 Mbps", "10 Mbps"});
        networkLayout->addRow("Bandwidth Limit:", bandwidthLimit);

        tabs->addTab(generalTab, "General");
        tabs->addTab(networkTab, "Network");

        QVBoxLayout* dialogLayout = new QVBoxLayout(&prefsDialog);
        dialogLayout->addWidget(tabs);
        QDialogButtonBox* buttonBox = new QDialogButtonBox(
            QDialogButtonBox::Ok | QDialogButtonBox::Cancel);
        dialogLayout->addWidget(buttonBox);
        QObject::connect(buttonBox, &QDialogButtonBox::accepted, &prefsDialog,
                         &QDialog::accept);
        QObject::connect(buttonBox, &QDialogButtonBox::rejected, &prefsDialog,
                         &QDialog::reject);
        prefsDialog.resize(400, 300);
        prefsDialog.exec();
    });

    // Peers menu
    QMenu* peersMenu           = menuBar->addMenu("Peers");
    QAction* managePeersAction = peersMenu->addAction("Manage Peers");
    QObject::connect(managePeersAction, &QAction::triggered, [window]() {
        QDialog peersDialog(window);
        peersDialog.setWindowTitle("Manage Peers");
        QVBoxLayout* dialogLayout = new QVBoxLayout(&peersDialog);
        QTableWidget* peersList   = new QTableWidget(3, 3);
        QStringList headers       = {"Username", "Role", "Actions"};
        peersList->setHorizontalHeaderLabels(headers);
        peersList->setItem(0, 0, new QTableWidgetItem("User1"));
        peersList->setItem(0, 1, new QTableWidgetItem("Viewer"));
        peersList->setItem(1, 0, new QTableWidgetItem("User2"));
        peersList->setItem(1, 1, new QTableWidgetItem("Streamer"));
        peersList->setItem(2, 0, new QTableWidgetItem("User3"));
        peersList->setItem(2, 1, new QTableWidgetItem("Viewer"));
        peersList->setColumnWidth(0, 150);
        peersList->setColumnWidth(1, 100);
        peersList->setColumnWidth(2, 100);
        dialogLayout->addWidget(peersList);
        QHBoxLayout* buttonLayout = new QHBoxLayout();
        QPushButton* closeButton  = new QPushButton("Close");
        QObject::connect(closeButton, &QPushButton::clicked, &peersDialog,
                         &QDialog::close);
        buttonLayout->addStretch();
        buttonLayout->addWidget(closeButton);
        buttonLayout->addStretch();
        dialogLayout->addLayout(buttonLayout);
        peersDialog.resize(400, 300);
        peersDialog.exec();
    });

    // View menu
    QMenu* viewMenu              = menuBar->addMenu("View");
    QAction* toggleSidebarAction = viewMenu->addAction("Toggle Sidebar");
    // Use a static variable to persist the sidebar state across invocations
    static bool sidebarVisible = true;
    SidebarToggleCallback      = [window, mainSplitter, rightPanel]() mutable {
        sidebarVisible = !sidebarVisible;
        if (sidebarVisible) {
            rightPanel->show();
            QList<int> sizes;
            sizes << int(window->width() * 0.65) << int(window->width() * 0.35);
            mainSplitter->setSizes(sizes);
        } else {
            rightPanel->hide();
            QList<int> sizes;
            sizes << window->width() << 0;
            mainSplitter->setSizes(sizes);
        }
    };
    QObject::connect(toggleSidebarAction, &QAction::triggered, []() {
        if (SidebarToggleCallback)
            SidebarToggleCallback();
    });

    // Roles menu
    QMenu* rolesMenu = menuBar->addMenu("Roles");

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
                         qDebug()
                             << "MENUS: definitionsChanged signal received. "
                                "Rebuilding roles menu for local peer ("
                             << roleStore->getLocalPeerId() << ").";
                         rebuildRolesMenu(rolesMenu, roleStore, worker, window);
                     });

    QObject::connect(
        roleStore, &P2P::Roles::RoleStore::peerRolesChanged, window,
        [rolesMenu, roleStore, worker, window](const QString& changedPeerId) {
            QString localPeerId = roleStore->getLocalPeerId();
            if (localPeerId == changedPeerId && !localPeerId.isEmpty()) {
                qDebug()
                    << "MENUS: peerRolesChanged signal received for myPeerId ("
                    << changedPeerId << "). Rebuilding roles menu.";
                rebuildRolesMenu(rolesMenu, roleStore, worker, window);
            }
        });

    QObject::connect(
        roleStore, &P2P::Roles::RoleStore::allAssignmentsRefreshed, window,
        [rolesMenu, roleStore, worker, window]() {
            qDebug() << "MENUS: allAssignmentsRefreshed signal received. "
                        "Rebuilding roles menu for local peer ("
                     << roleStore->getLocalPeerId() << ").";
            rebuildRolesMenu(rolesMenu, roleStore, worker, window);
        });

    QObject::connect(
        roleStore, &P2P::Roles::RoleStore::localPeerIdConfirmed, window,
        [rolesMenu, roleStore, worker, window](const QString& /*localId*/) {
            qDebug() << "MENUS: localPeerIdConfirmed signal received. "
                        "Rebuilding roles menu for local peer ("
                     << roleStore->getLocalPeerId() << ").";
            rebuildRolesMenu(rolesMenu, roleStore, worker, window);
        });

    // Help menu
    QMenu* helpMenu      = menuBar->addMenu("Help");
    QAction* aboutAction = helpMenu->addAction("About");
    QObject::connect(aboutAction, &QAction::triggered, [window]() {
        QMessageBox::information(
            window, "About",
            "P2PTogether v0.1\n\nA synchronized viewing platform prototype");
    });
}

} // namespace gui
