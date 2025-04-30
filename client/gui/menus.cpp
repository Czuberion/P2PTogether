#include "menus.h"
#include "gui/right_panel.h"
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

void createMenus(QMainWindow* window, P2P::Peer* peer, QSplitter* mainSplitter,
                 QWidget* rightPanel, QWidget* leftPanel) {
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
        peer->cleanup();
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
    QMenu* rolesMenu      = menuBar->addMenu("Roles");
    QAction* viewerAction = rolesMenu->addAction("Viewer");
    QObject::connect(viewerAction, &QAction::triggered, [peer, window]() {
        peer->roles = {P2P::Role::Viewer};
        QMessageBox::information(window, "Role Changed",
                                 "Your role has been changed to: Viewer");
        if (gui::QueueButtonsRefreshCallback)
            gui::QueueButtonsRefreshCallback();
    });
    QAction* streamerAction = rolesMenu->addAction("Streamer");
    QObject::connect(streamerAction, &QAction::triggered, [peer, window]() {
        peer->roles = {P2P::Role::Streamer};
        QMessageBox::information(window, "Role Changed",
                                 "Your role has been changed to: Streamer");
        if (gui::QueueButtonsRefreshCallback)
            gui::QueueButtonsRefreshCallback();
    });
    QAction* moderatorAction = rolesMenu->addAction("Moderator");
    QObject::connect(moderatorAction, &QAction::triggered, [peer, window]() {
        peer->roles = {P2P::Role::Moderator};
        QMessageBox::information(window, "Role Changed",
                                 "Your role has been changed to: Moderator");
        if (gui::QueueButtonsRefreshCallback)
            gui::QueueButtonsRefreshCallback();
    });

    QAction* adminAction = rolesMenu->addAction("Admin");
    QObject::connect(adminAction, &QAction::triggered, [peer, window]() {
        peer->roles = {P2P::Role::Admin};
        QMessageBox::information(window, "Role Changed",
                                 "Your role has been changed to: Admin");
        if (gui::QueueButtonsRefreshCallback)
            gui::QueueButtonsRefreshCallback();
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
