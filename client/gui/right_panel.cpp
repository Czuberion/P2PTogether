#include "right_panel.h"
#include "client.pb.h"
#include "p2p/peer.h"
#include "roles/permissions.h"
#include "transport/control_stream_worker.h"

#include <QCheckBox>
#include <QDateTime>
#include <QDebug>
#include <QDir>
#include <QFileDialog>
#include <QFileInfo>
#include <QFormLayout>
#include <QGroupBox>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QMessageBox>
#include <QPushButton>
#include <QSplitter>
#include <QTabWidget>
#include <QTableWidget>
#include <QTextEdit>
#include <QVBoxLayout>
#include <QWidget>

namespace gui {

std::function<void()> QueueButtonsRefreshCallback;

QWidget* createRightPanel(gui::App* app, QMainWindow* window,
                          P2P::ControlStreamWorker* worker,
                          P2P::Roles::RoleStore* roleStore) {
    QWidget* rightPanel = new QWidget();
    QVBoxLayout* layout = new QVBoxLayout(rightPanel);
    rightPanel->setLayout(layout);
    rightPanel->setMinimumWidth(300);

    // Create tabs
    QTabWidget* tabs = new QTabWidget();
    tabs->setObjectName("rightPanelTabs");

    // Chat & Queue Tab
    QWidget* chatQueueWidget     = new QWidget();
    QVBoxLayout* chatQueueLayout = new QVBoxLayout(chatQueueWidget);
    chatQueueWidget->setLayout(chatQueueLayout);

    // Create chat & queue splitter
    QSplitter* chatQueueSplitter = new QSplitter(Qt::Vertical);

    // Chat widget
    QWidget* chatWidget     = new QWidget();
    QVBoxLayout* chatLayout = new QVBoxLayout(chatWidget);
    chatWidget->setLayout(chatLayout);

    // Chat messages area
    QTextEdit* chatMessages = new QTextEdit();
    chatMessages->setReadOnly(true);
    chatMessages->setMinimumHeight(200);

    // Chat input area
    QWidget* chatInputWidget     = new QWidget();
    QHBoxLayout* chatInputLayout = new QHBoxLayout(chatInputWidget);
    chatInputWidget->setLayout(chatInputLayout);
    QLineEdit* chatInput = new QLineEdit();
    chatInput->setPlaceholderText("Type a message...");
    QPushButton* sendButton = new QPushButton("Send");
    chatInputLayout->addWidget(chatInput, 1);
    chatInputLayout->addWidget(sendButton);
    QObject::connect(
        sendButton, &QPushButton::clicked, [chatInput, chatMessages, worker]() {
            QString msg = chatInput->text();
            if (!msg.isEmpty()) {
                if (worker) {
                    client::ClientMsg clientMsg;
                    auto* chatCmdProto = clientMsg.mutable_chat_cmd();
                    chatCmdProto->set_text(msg.toStdString());
                    chatCmdProto->set_hlc_ts(
                        QDateTime::currentMSecsSinceEpoch());
                    worker->send(clientMsg);
                }
                chatInput->clear();
            }
        });
    QObject::connect(chatInput, &QLineEdit::returnPressed,
                     [chatInput, chatMessages, worker]() {
                         QString msg = chatInput->text();
                         if (!msg.isEmpty()) {
                             if (worker) {
                                 client::ClientMsg clientMsg;
                                 auto* chatCmdProto =
                                     clientMsg.mutable_chat_cmd();
                                 chatCmdProto->set_text(msg.toStdString());
                                 chatCmdProto->set_hlc_ts(
                                     QDateTime::currentMSecsSinceEpoch());
                                 worker->send(clientMsg);
                             }
                             chatInput->clear();
                         }
                     });
    QObject::connect(
        app, &gui::App::chatMessageReceived, chatMessages,
        [chatMessages](const QString& senderName, const QString& messageText,
                       bool /*isSelf*/) {
            QString formattedMessage = QString("<b>%1</b>: %2")
                                           .arg(senderName.toHtmlEscaped(),
                                                messageText.toHtmlEscaped());
            chatMessages->append(formattedMessage);
        });
    chatLayout->addWidget(chatMessages);
    chatLayout->addWidget(chatInputWidget);

    // Queue widget
    QWidget* queueWidget     = new QWidget();
    QVBoxLayout* queueLayout = new QVBoxLayout(queueWidget);
    queueWidget->setLayout(queueLayout);

    // Queue header
    QWidget* queueHeaderWidget     = new QWidget();
    QHBoxLayout* queueHeaderLayout = new QHBoxLayout(queueHeaderWidget);
    queueHeaderLayout->setContentsMargins(0, 4, 0, 4);
    queueHeaderWidget->setLayout(queueHeaderLayout);
    QLabel* queueLabel = new QLabel("Video Queue");
    queueLabel->setAlignment(Qt::AlignCenter);
    QPushButton* toggleQueueBtn = new QPushButton("▼");
    toggleQueueBtn->setFixedSize(40, 40);
    toggleQueueBtn->setStyleSheet("font-size: 20px; text-align: center;");
    toggleQueueBtn->setToolTip("Toggle queue visibility");
    queueHeaderWidget->setSizePolicy(QSizePolicy::Expanding,
                                     QSizePolicy::Preferred);
    const int minH = toggleQueueBtn->height() +
                     queueHeaderLayout->contentsMargins().top() +
                     queueHeaderLayout->contentsMargins().bottom();
    queueHeaderWidget->setMinimumHeight(minH);
    queueHeaderLayout->addWidget(queueLabel, 1);
    queueHeaderLayout->addWidget(toggleQueueBtn, 0);

    // Queue list
    QListWidget* queueList = new QListWidget();
    queueList->setMinimumHeight(100);

    // Queue buttons
    QWidget* queueButtonsWidget     = new QWidget();
    QHBoxLayout* queueButtonsLayout = new QHBoxLayout(queueButtonsWidget);
    queueButtonsWidget->setLayout(queueButtonsLayout);

    // Sub‑widget that contains add / remove / clear
    // (visible only when we have Queue permission)
    QWidget* manipButtonsWidget     = new QWidget(queueButtonsWidget);
    QHBoxLayout* manipButtonsLayout = new QHBoxLayout(manipButtonsWidget);
    manipButtonsLayout->setContentsMargins(0, 0, 0, 0);
    manipButtonsWidget->setLayout(manipButtonsLayout);

    // Queue visibility flag
    static bool queueVisible = true;

    // queue‑manipulation buttons (gated by PermQueue)
    QPushButton* addBtn = new QPushButton("+");
    addBtn->setFixedSize(40, 40);
    addBtn->setStyleSheet("font-size: 20px; text-align: center;");
    addBtn->setToolTip("Add a video to the queue");

    QPushButton* removeBtn = new QPushButton("-");
    removeBtn->setFixedSize(40, 40);
    removeBtn->setStyleSheet("font-size: 20px; text-align: center;");
    removeBtn->setToolTip("Remove selected video from the queue");
    removeBtn->setEnabled(queueList->count() > 0);

    QPushButton* clearBtn = new QPushButton("✕");
    clearBtn->setFixedSize(40, 40);
    clearBtn->setStyleSheet("font-size: 20px; text-align: center;");
    clearBtn->setToolTip("Clear the queue");
    clearBtn->setEnabled(queueList->count() > 0);

    // Info button (visible for all)
    QPushButton* infoBtn = new QPushButton("🛈");
    infoBtn->setFixedSize(40, 40);
    infoBtn->setStyleSheet("font-size: 20px; text-align: center;");
    infoBtn->setToolTip("Show information about the selected item");
    infoBtn->setEnabled(queueList->count() > 0);

    manipButtonsLayout->addWidget(addBtn);
    manipButtonsLayout->addWidget(removeBtn);
    manipButtonsLayout->addWidget(clearBtn);

    queueButtonsLayout->addStretch();
    queueButtonsLayout->addWidget(manipButtonsWidget);
    queueButtonsLayout->addWidget(infoBtn);
    queueButtonsLayout->addStretch();

    // Visibility refresh lambda (now that every widget exists)
    auto refreshQueueButtonsVisibility = [roleStore, manipButtonsWidget,
                                          infoBtn, queueList]() {
        const QString me = roleStore->getLocalPeerId();
        const bool canQueue =
            (roleStore->getPermissionsForPeer(me) & P2P::Roles::PermQueue) != 0;

        manipButtonsWidget->setVisible(canQueue && queueVisible);

        const bool infoEnabled = queueVisible && queueList->currentRow() >= 0;
        infoBtn->setEnabled(infoEnabled);
        infoBtn->setVisible(queueVisible);
    };
    QueueButtonsRefreshCallback = refreshQueueButtonsVisibility;
    refreshQueueButtonsVisibility();

    // Connect to RoleStore signals to refresh button visibility when
    // roles/permissions change
    QObject::connect(roleStore, &P2P::Roles::RoleStore::peerRolesChanged,
                     queueButtonsWidget,
                     [refreshQueueButtonsVisibility](const QString&) {
                         refreshQueueButtonsVisibility();
                     });

    QObject::connect(roleStore, &P2P::Roles::RoleStore::definitionsChanged,
                     queueButtonsWidget, refreshQueueButtonsVisibility);

    QObject::connect(roleStore, &P2P::Roles::RoleStore::allAssignmentsRefreshed,
                     queueButtonsWidget, refreshQueueButtonsVisibility);

    QObject::connect(roleStore, &P2P::Roles::RoleStore::localPeerIdConfirmed,
                     queueButtonsWidget,
                     [refreshQueueButtonsVisibility](const QString&) {
                         refreshQueueButtonsVisibility();
                     });

    // Lambda to refresh the QListWidget display (with play indicator)
    auto refreshQueueListDisplay = [app, queueList, clearBtn]() {
        if (!app || !queueList)
            return;

        queueList->clear();
        QList<client::p2p::QueueItem> currentItems =
            app->getCurrentQueueItems();
        int playingIndex = app->getCurrentPlayingIndex();

        for (int i = 0; i < currentItems.size(); ++i) {
            const auto& item       = currentItems.at(i);
            const QString fullPath = QString::fromStdString(item.file_path());
            QString displayName    = QFileInfo(fullPath).fileName();

            if (i == playingIndex) {
                displayName = QString("[▶] ") + displayName;
            }

            auto* listItem = new QListWidgetItem(displayName);
            listItem->setData(Qt::UserRole, fullPath); // Store full path
            listItem->setData(Qt::UserRole + 1,
                              QString::fromStdString(item.stored_by()));
            listItem->setData(Qt::UserRole + 2,
                              QString::fromStdString(item.added_by()));
            // Store original index for potential re-identification if needed,
            // though not used by setText directly
            listItem->setData(Qt::UserRole + 3, i);
            queueList->addItem(listItem);
        }

        if (clearBtn) {
            clearBtn->setEnabled(queueList->count() > 0);
        }
        // Note: removeBtn and infoBtn enablement is handled by
        // currentRowChanged signal of queueList
    };

    // Connect to the worker's serverMsg signal to handle QueueUpdates
    if (worker) {
        QObject::connect(
            worker, &P2P::ControlStreamWorker::serverMsg, queueList,
            // [queueList, clearBtn](const client::ServerMsg& msg) {
            [app, refreshQueueListDisplay](const client::ServerMsg& msg) {
                if (msg.payload_case() == client::ServerMsg::kQueueUpdate) {
                    qDebug() << "GUI: Received QueueUpdate";
                    // App's onServerMessage already updates m_currentQueueItems
                    // and emits queueStateChanged. That signal will trigger
                    // refreshQueueListDisplay. So, no direct action needed here
                    // on queueList, but ensure App processes kQueueUpdate
                    // *before* right_panel might try to use stale data. For
                    // safety, or if App's signal isn't connected yet / order
                    // issues: refreshQueueListDisplay(); // Or rely on App's
                    // signal
                }
            },
            Qt::QueuedConnection); // QueuedConnection is good for cross-thread
                                   // signals
    }

    // Connect to App's signal to refresh queue display when playing index or
    // items change
    QObject::connect(app, &gui::App::queueStateChanged, queueList,
                     refreshQueueListDisplay);
    refreshQueueListDisplay(); // Initial population

    QObject::connect(toggleQueueBtn, &QPushButton::clicked,
                     [toggleQueueBtn, queueList, queueButtonsWidget,
                      queueWidget, queueHeaderWidget]() mutable {
                         queueVisible = !queueVisible;
                         if (queueVisible) {
                             queueList->show();
                             toggleQueueBtn->setText("▼");
                             queueWidget->setMaximumHeight(QWIDGETSIZE_MAX);
                         } else {
                             queueList->hide();
                             toggleQueueBtn->setText("▲");
                             // Use the header’s minimumHeight (which already
                             // includes your contentsMargins) so you don’t
                             // accidentally chop off a few px...
                             int headerHeight =
                                 queueHeaderWidget->minimumHeight();
                             int topMargin, bottomMargin;
                             queueWidget->layout()->getContentsMargins(
                                 nullptr, &topMargin, nullptr, &bottomMargin);
                             queueWidget->setMaximumHeight(
                                 headerHeight + topMargin + bottomMargin);
                         }
                         QueueButtonsRefreshCallback();
                     });

    // Use a non-blocking, non-native dialog so mpv’s GL thread doesn’t
    // interfere with the OS-level file-chooser.  The dialog is destroyed
    // automatically after use.
    QObject::connect(addBtn, &QPushButton::clicked, [window, worker]() {
        auto* dialog = new QFileDialog(window);
        dialog->setWindowTitle("Open Video File");

        QStringList filters;
        filters << "Video Files (*.mp4 *.mkv *.avi *.mov)"
                << "All Files (*)";
        dialog->setNameFilters(filters);

        dialog->setFileMode(QFileDialog::ExistingFile);
        dialog->setOption(QFileDialog::DontUseNativeDialog); // critical!

        QObject::connect(dialog, &QFileDialog::fileSelected,
                         [dialog, worker](const QString& filePath) {
                             dialog->deleteLater();
                             if (filePath.isEmpty() || !worker)
                                 return;

                             client::ClientMsg clientMsg;
                             auto* cmd = clientMsg.mutable_queue_cmd();
                             cmd->set_type(client::p2p::QueueCmd_Type_APPEND);
                             cmd->set_file_path(filePath.toStdString());

                             qDebug() << "GUI: Sending APPEND QueueCmd for"
                                      << filePath;
                             worker->send(clientMsg);
                         });

        dialog->open(); // non-modal → UI stays responsive
    });

    QObject::connect(removeBtn, &QPushButton::clicked, [queueList, worker]() {
        int row = queueList->currentRow();
        if (row >= 0) {
            if (worker) {
                client::ClientMsg clientMsg;
                client::p2p::QueueCmd* cmd = clientMsg.mutable_queue_cmd();
                cmd->set_type(client::p2p::QueueCmd_Type_REMOVE);
                cmd->set_index(static_cast<int32_t>(row));
                // cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());

                qDebug() << "GUI: Sending REMOVE QueueCmd for index" << row;
                worker->send(clientMsg);
            }
            // The QListWidget will be updated when ServerMsg_QueueUpdate is
            // received.
        }
    });

    QObject::connect(clearBtn, &QPushButton::clicked, [queueList, worker]() {
        if (worker) {
            client::ClientMsg clientMsg;
            client::p2p::QueueCmd* cmd = clientMsg.mutable_queue_cmd();
            cmd->set_type(client::p2p::QueueCmd_Type_CLEAR);
            // cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());

            qDebug() << "GUI: Sending CLEAR QueueCmd";
            worker->send(clientMsg);
        }
        // The QListWidget will be updated when ServerMsg_QueueUpdate is
        // received.
    });

    QObject::connect(queueList, &QListWidget::currentRowChanged,
                     [removeBtn, infoBtn](int row) {
                         const bool valid = row >= 0;
                         removeBtn->setEnabled(valid);
                         infoBtn->setEnabled(valid);
                     });

    // Info-button behaviour
    QObject::connect(
        infoBtn, &QPushButton::clicked, [queueList, window, roleStore]() {
            int row = queueList->currentRow();
            if (row < 0)
                return;

            QListWidgetItem* it      = queueList->item(row);
            const QString filePath   = it->data(Qt::UserRole).toString();
            const QString storedById = it->data(Qt::UserRole + 1).toString();
            const QString addedById  = it->data(Qt::UserRole + 2).toString();
            const QString fileName   = QFileInfo(filePath).fileName();

            QDialog dlg(window);
            dlg.setWindowTitle(QString("Queue Item - %1").arg(fileName));

            QFormLayout form(&dlg);

            auto formatPeerInfo = [&](const QString& peerId) -> QString {
                if (peerId.isEmpty())
                    return "‑";
                QString username = roleStore->getPeerUsername(peerId);
                if (!username.isEmpty()) {
                    return QString("%1 (%2)").arg(username, peerId);
                }
                return peerId;
            };

            QLabel* filePathLabel = new QLabel(filePath, &dlg);
            filePathLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
            form.addRow("File path:", filePathLabel);

            QLabel* storedByLabel =
                new QLabel(formatPeerInfo(storedById), &dlg);
            storedByLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
            form.addRow("Stored by:", storedByLabel);

            QLabel* addedByLabel = new QLabel(formatPeerInfo(addedById), &dlg);
            addedByLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
            form.addRow("Requested by:", addedByLabel);

            QDialogButtonBox box(QDialogButtonBox::Ok, &dlg);
            form.addRow(&box);
            QObject::connect(&box, &QDialogButtonBox::accepted, &dlg,
                             &QDialog::accept);

            dlg.exec();
        });

    queueLayout->addWidget(queueHeaderWidget);
    queueLayout->addWidget(queueList);
    queueLayout->addWidget(queueButtonsWidget);

    chatQueueSplitter->addWidget(chatWidget);
    chatQueueSplitter->addWidget(queueWidget);
    chatQueueSplitter->setSizes({80, 20});
    chatQueueSplitter->setChildrenCollapsible(false);
    chatQueueLayout->addWidget(chatQueueSplitter);

    // Additional tabs: Peers and Analytics
    QWidget* peersTab           = new QWidget();
    QVBoxLayout* peersTabLayout = new QVBoxLayout(peersTab);

    QTableWidget* peersTable = new QTableWidget();
    peersTable->setColumnCount(2);
    peersTable->setHorizontalHeaderLabels({"Username", "Actions"});
    peersTable->horizontalHeader()->setStretchLastSection(true);
    peersTable->setEditTriggers(QAbstractItemView::NoEditTriggers);
    peersTable->setSelectionBehavior(QAbstractItemView::SelectRows);
    peersTable->setSelectionMode(QAbstractItemView::SingleSelection);
    peersTable->setColumnWidth(0, 150);
    peersTabLayout->addWidget(peersTable);

    auto rebuildPeersTable = [peersTable, roleStore, worker, window, app]() {
        peersTable->clearContents();
        peersTable->setRowCount(0);

        QVector<QString> peerIds = roleStore->getKnownPeerIds();
        const QString myId       = roleStore->getLocalPeerId();
        const quint32 myPerms    = roleStore->getPermissionsForPeer(myId);
        const bool canManageRoles =
            P2P::Roles::hasPermission(myPerms, P2P::Roles::PermManageUserRoles);
        const bool canKick =
            P2P::Roles::hasPermission(myPerms, P2P::Roles::PermKickUser);
        const bool canBan =
            P2P::Roles::hasPermission(myPerms, P2P::Roles::PermBanUser);

        peersTable->setRowCount(peerIds.size());
        int row = 0;
        for (const QString& peerId : peerIds) {
            QString username = roleStore->getPeerUsername(peerId);
            if (username.isEmpty())
                username = peerId.left(12) + "...";
            if (peerId == myId)
                username += " (You)";

            peersTable->setItem(row, 0, new QTableWidgetItem(username));

            QWidget* actionsWidget     = new QWidget();
            QHBoxLayout* actionsLayout = new QHBoxLayout(actionsWidget);
            actionsLayout->setContentsMargins(2, 2, 2, 2);
            actionsLayout->setSpacing(4);

            QPushButton* infoBtn = new QPushButton("Info");
            actionsLayout->addWidget(infoBtn);
            QObject::connect(infoBtn, &QPushButton::clicked, window, [=]() {
                QDialog infoDialog(window);
                infoDialog.setWindowTitle(QString("Info for %1").arg(username));
                QFormLayout* form = new QFormLayout(&infoDialog);

                QLabel* usernameLabel =
                    new QLabel(roleStore->getPeerUsername(peerId));
                usernameLabel->setTextInteractionFlags(
                    Qt::TextSelectableByMouse);
                form->addRow("Username:", usernameLabel);

                QLabel* peerIdLabel = new QLabel(peerId);
                peerIdLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
                form->addRow("Peer ID:", peerIdLabel);

                QString rolesStr =
                    roleStore->getAssignedRoleNames(peerId).join(", ");
                QLabel* rolesLabel =
                    new QLabel(rolesStr.isEmpty() ? "(none)" : rolesStr);
                rolesLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
                form->addRow("Roles:", rolesLabel);

                QDialogButtonBox* buttonBox =
                    new QDialogButtonBox(QDialogButtonBox::Ok, &infoDialog);
                form->addRow(buttonBox);
                QObject::connect(buttonBox, &QDialogButtonBox::accepted,
                                 &infoDialog, &QDialog::accept);
                infoDialog.exec();
            });

            if (canManageRoles) {
                QPushButton* rolesBtn = new QPushButton("Roles");
                actionsLayout->addWidget(rolesBtn);

                QObject::connect(
                    rolesBtn, &QPushButton::clicked, window, [=]() {
                        QDialog roleDialog(window);
                        roleDialog.setWindowTitle(
                            QString("Set Roles for %1").arg(username));
                        QVBoxLayout* dLayout = new QVBoxLayout(&roleDialog);

                        QGroupBox* rolesBox =
                            new QGroupBox("Available Roles", &roleDialog);
                        QVBoxLayout* rolesLayout = new QVBoxLayout();
                        rolesBox->setLayout(rolesLayout);
                        dLayout->addWidget(rolesBox);

                        QMap<QString, QCheckBox*> roleCheckBoxes;
                        QVector<QString> currentPeerRoles =
                            roleStore->getAssignedRoleNames(peerId);
                        for (const auto& roleDef :
                             roleStore->getRoleDefinitions()) {
                            QString roleName =
                                QString::fromStdString(roleDef.name());
                            QCheckBox* cb = new QCheckBox(roleName);
                            cb->setChecked(
                                currentPeerRoles.contains(roleName.toLower()));

                            if (!P2P::Roles::hasPermission(
                                    myPerms, (P2P::Roles::Permission)
                                                 roleDef.permissions())) {
                                cb->setDisabled(true);
                                cb->setToolTip(
                                    "You cannot grant this role as it contains "
                                    "permissions you do not possess.");
                            }
                            rolesLayout->addWidget(cb);
                            roleCheckBoxes[roleName.toLower()] = cb;
                        }

                        QDialogButtonBox* buttonBox = new QDialogButtonBox(
                            QDialogButtonBox::Ok | QDialogButtonBox::Cancel,
                            &roleDialog);
                        dLayout->addWidget(buttonBox);
                        QObject::connect(buttonBox, &QDialogButtonBox::accepted,
                                         &roleDialog, &QDialog::accept);
                        QObject::connect(buttonBox, &QDialogButtonBox::rejected,
                                         &roleDialog, &QDialog::reject);

                        if (roleDialog.exec() == QDialog::Accepted) {
                            client::ClientMsg msg;
                            auto* cmd = msg.mutable_set_peer_roles_cmd();
                            cmd->set_target_peer_id(peerId.toStdString());
                            for (auto it = roleCheckBoxes.constBegin();
                                 it != roleCheckBoxes.constEnd(); ++it) {
                                if (it.value()->isChecked()) {
                                    cmd->add_assigned_role_names(
                                        it.key().toStdString());
                                }
                            }
                            worker->send(msg);
                        }
                    });
            }

            if (canKick && peerId != myId) {
                QPushButton* kickBtn = new QPushButton("Kick");
                actionsLayout->addWidget(kickBtn);
                QObject::connect(kickBtn, &QPushButton::clicked, window, [=]() {
                    QMessageBox::information(
                        window, "Not Implemented",
                        "Kicking users is not yet implemented.");
                });
            }

            if (canBan && peerId != myId) {
                QPushButton* banBtn = new QPushButton("Ban");
                actionsLayout->addWidget(banBtn);
                QObject::connect(banBtn, &QPushButton::clicked, window, [=]() {
                    QMessageBox::information(
                        window, "Not Implemented",
                        "Banning users is not yet implemented.");
                });
            }

            actionsLayout->addStretch();
            peersTable->setCellWidget(row, 1, actionsWidget);
            row++;
        }
    };

    QObject::connect(roleStore, &P2P::Roles::RoleStore::allAssignmentsRefreshed,
                     peersTable, rebuildPeersTable);
    QObject::connect(roleStore, &P2P::Roles::RoleStore::peerRolesChanged,
                     peersTable, rebuildPeersTable);
    rebuildPeersTable();

    QWidget* analyticsTab        = new QWidget();
    QVBoxLayout* analyticsLayout = new QVBoxLayout(analyticsTab);
    analyticsTab->setLayout(analyticsLayout);
    QLabel* header = new QLabel("Session Statistics");
    header->setAlignment(Qt::AlignCenter);
    analyticsLayout->addWidget(header);
    analyticsLayout->addWidget(new QLabel("Connected: 3 peers"));
    analyticsLayout->addWidget(new QLabel("Uptime: 0h 42m"));
    analyticsLayout->addWidget(new QLabel("Streaming bitrate: 2.5 Mbps"));
    analyticsLayout->addWidget(new QLabel("Network usage: 632 MB"));
    analyticsLayout->addStretch();

    tabs->addTab(chatQueueWidget, "Chat + Queue");
    tabs->addTab(peersTab, "Peers");
    tabs->addTab(analyticsTab, "Analytics");

    layout->addWidget(tabs);
    return rightPanel;
}

} // namespace gui
