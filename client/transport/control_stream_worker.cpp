#include "control_stream_worker.h"
#include <QDebug> // Include QDebug for logging
#include <QMetaObject>
#include <QThread> // Include QThread for currentThreadId()

namespace P2P {

ControlStreamWorker::ControlStreamWorker(std::unique_ptr<GrpcClient> client,
                                         QObject* parent) :
    QObject(parent), client_(std::move(client)) {}

ControlStreamWorker::~ControlStreamWorker() {
    qDebug() << "ControlStreamWorker::~ControlStreamWorker() called";
    quit_ = true; // Ensure quit flag is set, though stop() should handle it
}

void ControlStreamWorker::run() {
    qDebug() << "ControlStreamWorker::run() started in thread:" << QThread::currentThreadId();
    try {
        ctx_    = std::make_unique<grpc::ClientContext>();
        stream_ = client_->openControlStream(*ctx_);
        if (!stream_) { // Check if stream creation failed
             emit finished(QString("Failed to open stream: stream_ is null"));
             qWarning() << "ControlStreamWorker::run() - stream_ is null after openControlStream";
             return;
        }
         qDebug() << "ControlStreamWorker::run() - Stream opened.";
    } catch (const std::exception& e) {
        emit finished(QString("Failed to open stream: %1").arg(e.what()));
         qWarning() << "ControlStreamWorker::run() - Exception opening stream:" << e.what();
        return;
    }

    p2p::ServerMsg in;
    qDebug() << "ControlStreamWorker::run() - Entering Read loop...";
    // Use a temporary variable to check Read result before checking quit_
    bool read_ok = false;
    while ( (read_ok = stream_->Read(&in)) && !quit_ ) { // Check read_ok first
        emit serverMsg(in); // ← producer pops, GUI consumes
    }
    // Log why the loop exited
    if (quit_) {
         qDebug() << "ControlStreamWorker::run() - Exited Read loop because quit_ is true.";
    } else if (!read_ok) {
         qDebug() << "ControlStreamWorker::run() - Exited Read loop because stream_->Read() returned false.";
    }


    qDebug() << "ControlStreamWorker::run() - Calling stream_->Finish()...";
    grpc::Status st = stream_->Finish(); // This sends FIN to the server
    qDebug() << "ControlStreamWorker::run() - stream_->Finish() completed with status:"
             << (st.ok() ? "OK" : QString::fromStdString(st.error_message()));

    emit finished(
        QString("stream closed: %1")
            .arg(st.ok() ? "OK" : QString::fromStdString(st.error_message())));
     qDebug() << "ControlStreamWorker::run() finished.";
}

void ControlStreamWorker::start() {
    run();
}

void ControlStreamWorker::stop() {
    qDebug() << "ControlStreamWorker::stop() called in thread:" << QThread::currentThreadId();
    quit_ = true; // Signal the loop to exit
    if (ctx_) {
        qDebug() << "ControlStreamWorker::stop() - Calling ctx_->TryCancel()...";
        ctx_->TryCancel(); // Attempt to unblock Read() immediately
        qDebug() << "ControlStreamWorker::stop() - ctx_->TryCancel() called.";
    } else {
         qWarning() << "ControlStreamWorker::stop() - ctx_ is null, cannot cancel.";
    }
}

void ControlStreamWorker::send(const p2p::ClientMsg& msg) {
    std::lock_guard<std::mutex> lk(txMu_);
    if (stream_)
        stream_->Write(msg, grpc::WriteOptions());
}

} // namespace P2P
