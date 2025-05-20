#include "right_panel.h"
#include "p2p/peer.h"
#include "roles/permissions.h"
#include <QDebug>
#include <QDir>
#include <QFileDialog>
#include <QFileInfo>
#include <QFormLayout>
#include <QHBoxLayout>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QMessageBox>
#include <QPushButton>
#include <QSplitter>
#include <QTabWidget>
#include <QTextEdit>
#include <QVBoxLayout>
#include <QWidget>

namespace gui {

std::function<void()> QueueButtonsRefreshCallback;

QWidget* createRightPanel(P2P::Peer* peer, QMainWindow* window,
                          P2P::ControlStreamWorker* worker,
                          P2P::Roles::RoleStore* roleStore) {
    QWidget* rightPanel = new QWidget();
    QVBoxLayout* layout = new QVBoxLayout(rightPanel);
    rightPanel->setLayout(layout);
    rightPanel->setMinimumWidth(300);

    // Create tabs
    QTabWidget* tabs = new QTabWidget();

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
    chatMessages->append("System: Welcome to P2PTogether!");
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
    QObject::connect(sendButton, &QPushButton::clicked,
                     [chatInput, chatMessages]() {
                         QString msg = chatInput->text();
                         if (!msg.isEmpty()) {
                             chatMessages->append("Me: " + msg);
                             chatInput->clear();
                         }
                     });
    QObject::connect(chatInput, &QLineEdit::returnPressed,
                     [chatInput, chatMessages]() {
                         QString msg = chatInput->text();
                         if (!msg.isEmpty()) {
                             chatMessages->append("Me: " + msg);
                             chatInput->clear();
                         }
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

    // Connect to the worker's serverMsg signal to handle QueueUpdates
    if (worker) {
        QObject::connect(
            worker, &P2P::ControlStreamWorker::serverMsg, queueList,
            [queueList, clearBtn](const client::ServerMsg& msg) {
                if (msg.payload_case() == client::ServerMsg::kQueueUpdate) {
                    qDebug() << "GUI: Received QueueUpdate";
                    const client::p2p::QueueUpdate& update = msg.queue_update();
                    queueList->clear();
                    for (const auto& item : update.items()) {
                        const QString fullPath =
                            QString::fromStdString(item.file_path());
                        const QString fileName = QFileInfo(fullPath).fileName();

                        auto* listIt = new QListWidgetItem(fileName);
                        listIt->setData(Qt::UserRole, fullPath);
                        listIt->setData(
                            Qt::UserRole + 1,
                            QString::fromStdString(item.stored_by()));
                        listIt->setData(
                            Qt::UserRole + 2,
                            QString::fromStdString(item.added_by()));
                        queueList->addItem(listIt);
                    }
                    // Update clear button state after list is potentially
                    // modified
                    if (clearBtn) {
                        clearBtn->setEnabled(queueList->count() > 0);
                    }
                }
            },
            Qt::QueuedConnection); // QueuedConnection is good for cross-thread
                                   // signals
    }

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
    QObject::connect(infoBtn, &QPushButton::clicked, [queueList, window]() {
        int row = queueList->currentRow();
        if (row < 0)
            return;

        QListWidgetItem* it    = queueList->item(row);
        const QString filePath = it->data(Qt::UserRole).toString();
        const QString storedBy = it->data(Qt::UserRole + 1).toString();
        const QString addedBy  = it->data(Qt::UserRole + 2).toString();
        const QString fileName = QFileInfo(filePath).fileName();

        QDialog dlg(window);
        dlg.setWindowTitle(QString("Queue Item - %1").arg(fileName));

        QFormLayout form(&dlg);
        form.addRow("File path:", new QLabel(filePath));
        form.addRow("Stored by:",
                    new QLabel(storedBy.isEmpty() ? "‑" : storedBy));
        form.addRow("Requested by:",
                    new QLabel(addedBy.isEmpty() ? "‑" : addedBy));

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
    QWidget* peersTab        = new QWidget();
    QVBoxLayout* peersLayout = new QVBoxLayout(peersTab);
    peersTab->setLayout(peersLayout);
    peersLayout->addWidget(new QLabel("Connected Peers:"));
    QListWidget* peersList = new QListWidget();
    peersList->addItem("User1 (Viewer)");
    peersList->addItem("User2 (Streamer)");
    peersList->addItem("User3 (Viewer)");
    peersLayout->addWidget(peersList);

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
