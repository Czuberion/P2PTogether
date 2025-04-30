#include "right_panel.h"
#include "p2p/peer.h"
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

QWidget* createRightPanel(P2P::Peer* peer, QMainWindow* window) {
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
    queueList->addItem("Sample_video_1.mp4");
    queueList->addItem("My_favorite_movie.mkv");

    // Queue buttons (visible only when we have Queue permission)
    QWidget* queueButtonsWidget     = new QWidget();
    QHBoxLayout* queueButtonsLayout = new QHBoxLayout(queueButtonsWidget);

    auto refreshQueueButtonsVisibility = [peer, queueButtonsWidget]() {
        queueButtonsWidget->setVisible(P2P::hasQueuePermission(peer->roles));
    };

    QueueButtonsRefreshCallback = refreshQueueButtonsVisibility;

    queueButtonsWidget->setLayout(queueButtonsLayout);
    QPushButton* addBtn = new QPushButton("+");
    addBtn->setFixedSize(40, 40);
    addBtn->setStyleSheet("font-size: 20px; text-align: center;");
    addBtn->setToolTip("Add a video to the queue");
    QPushButton* removeBtn = new QPushButton("-");
    removeBtn->setFixedSize(40, 40);
    removeBtn->setStyleSheet("font-size: 20px; text-align: center;");
    removeBtn->setToolTip("Remove selected video from the queue");
    removeBtn->setEnabled(false);
    QPushButton* clearBtn = new QPushButton("✕");
    clearBtn->setFixedSize(40, 40);
    clearBtn->setStyleSheet("font-size: 20px; text-align: center;");
    clearBtn->setToolTip("Clear the queue");
    queueButtonsLayout->addStretch();
    queueButtonsLayout->addWidget(addBtn);
    queueButtonsLayout->addWidget(removeBtn);
    queueButtonsLayout->addWidget(clearBtn);
    queueButtonsLayout->addStretch();
    refreshQueueButtonsVisibility();

    // Use a static variable for queue visibility so it persists beyond this
    // function
    static bool queueVisible = true;
    QObject::connect(
        toggleQueueBtn, &QPushButton::clicked,
        [toggleQueueBtn, queueList, queueButtonsWidget, queueWidget]() mutable {
            queueVisible = !queueVisible;
            if (queueVisible) {
                queueList->show();
                queueButtonsWidget->show();
                toggleQueueBtn->setText("▼");
                queueWidget->setMaximumHeight(QWIDGETSIZE_MAX);
            } else {
                queueList->hide();
                queueButtonsWidget->hide();
                toggleQueueBtn->setText("▲");
                queueWidget->setMaximumHeight(queueList->sizeHint().height() +
                                              10);
            }
            QueueButtonsRefreshCallback();
        });

    QObject::connect(
        addBtn, &QPushButton::clicked, [peer, window, queueList]() {
            QString filePath = QFileDialog::getOpenFileName(
                window, "Open Video File", "",
                "Video Files (*.mp4 *.mkv *.avi *.mov);;All Files (*)");
            if (filePath.isEmpty())
                return;
            QFileInfo fi(filePath);
            QString filename = fi.fileName();
            queueList->addItem(filename);
            // If streamer, simulate starting streaming
            if (peer->hasRole(P2P::Role::Streamer)) {
                qDebug() << "Starting to stream file:" << filePath;
                if (!peer->startStreaming(filePath.toStdString()))
                    QMessageBox::critical(window, "Error",
                                          "Failed to start streaming");
            } else {
                QMessageBox::information(window, "Not streamer",
                                         "You do not have 'streamer' role, so "
                                         "you cannot broadcast this file.");
            }
        });
    QObject::connect(removeBtn, &QPushButton::clicked, [queueList]() {
        int row = queueList->currentRow();
        if (row >= 0) {
            delete queueList->takeItem(row);
        }
    });
    QObject::connect(clearBtn, &QPushButton::clicked,
                     [queueList]() { queueList->clear(); });

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
