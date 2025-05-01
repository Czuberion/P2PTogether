#include "control_stream_worker.h"
#include <QMetaObject>

namespace P2P {

ControlStreamWorker::ControlStreamWorker(std::unique_ptr<GrpcClient> client,
                                         QObject* parent) :
    QObject(parent), client_(std::move(client)) {}

ControlStreamWorker::~ControlStreamWorker() {
    quit_ = true;
}

void ControlStreamWorker::run() {
    try {
        stream_ = client_->openControlStream();
    } catch (const std::exception& e) {
        emit finished(QString("Failed to open stream: %1").arg(e.what()));
        return;
    }

    p2p::ServerMsg in;
    while (!quit_ && stream_->Read(&in)) {
        emit serverMsg(in); // ← producer pops, GUI consumes
    }

    grpc::Status st = stream_->Finish();
    emit finished(
        QString("stream closed: %1")
            .arg(st.ok() ? "OK" : QString::fromStdString(st.error_message())));
}

void ControlStreamWorker::send(const p2p::ClientMsg& msg) {
    std::lock_guard<std::mutex> lk(txMu_);
    if (stream_)
        stream_->Write(msg, grpc::WriteOptions());
}

} // namespace P2P
