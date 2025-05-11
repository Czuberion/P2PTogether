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
    queueHeaderWidget->setLayout(queueHeaderLayout);
    QLabel* queueLabel = new QLabel("Video Queue");
    queueLabel->setAlignment(Qt::AlignCenter);
    QPushButton* toggleQueueBtn = new QPushButton("▼");
    toggleQueueBtn->setFixedSize(40, 40);
    toggleQueueBtn->setStyleSheet("font-size: 20px; text-align: center;");
    toggleQueueBtn->setToolTip("Toggle queue visibility");
    queueHeaderLayout->addWidget(queueLabel, 1);
    queueHeaderLayout->addWidget(toggleQueueBtn, 0);

    // Queue list
    QListWidget* queueList = new QListWidget();
    queueList->setMinimumHeight(100);

    // Queue buttons (visible only when we have Queue permission)
    QWidget* queueButtonsWidget     = new QWidget();
    QHBoxLayout* queueButtonsLayout = new QHBoxLayout(queueButtonsWidget);
    queueButtonsWidget->setLayout(queueButtonsLayout);

    // Queue buttons and list visibility
    static bool queueVisible           = true;
    auto refreshQueueButtonsVisibility = [roleStore, queueButtonsWidget]() {
        const QString me = roleStore->getLocalPeerId(); // ← real ID once known
        const bool canQueue =
            (roleStore->getPermissionsForPeer(me) & P2P::Roles::PermQueue) != 0;
        queueButtonsWidget->setVisible(canQueue && queueVisible);
    };
    QueueButtonsRefreshCallback = refreshQueueButtonsVisibility;

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

    queueButtonsLayout->addStretch();
    queueButtonsLayout->addWidget(addBtn);
    queueButtonsLayout->addWidget(removeBtn);
    queueButtonsLayout->addWidget(clearBtn);
    queueButtonsLayout->addStretch();
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
                    queueList->clear(); // Clear existing items
                    for (const auto& item : update.items()) {
                        // Displaying only file_path for simplicity.
                        // You might want to format this string to show
                        // StoredBy, AddedBy etc. e.g., QString display_text =
                        // QString::fromStdString(item.file_path()) +
                        //                              " (by " +
                        //                              QString::fromStdString(item.added_by())
                        //                              + ")";
                        queueList->addItem(
                            QString::fromStdString(item.file_path()));
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
                             // Calculate minimum height needed for the header +
                             // layout margins
                             int headerHeight =
                                 queueHeaderWidget->sizeHint().height();
                             int topMargin, bottomMargin;
                             queueWidget->layout()->getContentsMargins(
                                 nullptr, &topMargin, nullptr, &bottomMargin);
                             queueWidget->setMaximumHeight(
                                 headerHeight + topMargin + bottomMargin);
                         }
                         QueueButtonsRefreshCallback();
                     });

    QObject::connect(
        addBtn, &QPushButton::clicked, [peer, window, queueList, worker]() {
            QString filePath = QFileDialog::getOpenFileName(
                window, "Open Video File", "",
                "Video Files (*.mp4 *.mkv *.avi *.mov);;All Files (*)");
            if (filePath.isEmpty())
                return;
            if (worker) {
                client::ClientMsg clientMsg;
                client::p2p::QueueCmd* cmd = clientMsg.mutable_queue_cmd();
                cmd->set_type(client::p2p::QueueCmd_Type_APPEND);
                cmd->set_file_path(filePath.toStdString());
                // HLC timestamp can be set here if needed, e.g.,
                // QDateTime::currentMSecsSinceEpoch()
                // cmd->set_hlc_ts(QDateTime::currentMSecsSinceEpoch());

                qDebug() << "GUI: Sending APPEND QueueCmd for" << filePath;
                // Use QMetaObject::invokeMethod to ensure send is called on the
                // worker's thread if createRightPanel is called from a
                // different thread than the worker's. However, worker->send is
                // thread-safe due to its internal mutex.
                worker->send(clientMsg);
            }
            // The QListWidget will be updated when ServerMsg_QueueUpdate is
            // received. So, we don't manually addItem here anymore.
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
                     [removeBtn](int row) { removeBtn->setEnabled(row >= 0); });

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
