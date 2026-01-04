#include "control_stream_worker.h"
#include <QDebug> // Include QDebug for logging
#include <QMetaObject>
#include <QThread> // Include QThread for currentThreadId()

namespace P2P {

ControlStreamWorker::ControlStreamWorker(std::unique_ptr<GrpcClient> client,
                                         QObject *parent)
    : QObject(parent), client_(std::move(client)) {}

ControlStreamWorker::~ControlStreamWorker() {
  qInfo() << "ControlStreamWorker::~ControlStreamWorker()";
  try {
    // client_ (unique_ptr<GrpcClient>) will be destroyed here.
    // This is where GrpcClient's destructor and its members (channel, stub)
    // get destroyed.
    client_.reset(); // Explicitly reset to trigger destruction within
                     // try-catch
    qInfo() << "ControlStreamWorker::~ControlStreamWorker() - client_ "
               "unique_ptr reset.";
  } catch (const std::exception &e) {
    qCritical() << "EXCEPTION during client_ destruction in "
                   "~ControlStreamWorker(): "
                << e.what();
  } catch (...) {
    qCritical() << "UNKNOWN EXCEPTION during client_ destruction in "
                   "~ControlStreamWorker().";
  }
  qInfo() << "ControlStreamWorker::~ControlStreamWorker() finished.";
}

void ControlStreamWorker::run() {
  qInfo() << "ControlStreamWorker::run() ENTERED in thread:"
          << QThread::currentThreadId();

  try {
    ctx_ = std::make_unique<grpc::ClientContext>();

    stream_ = client_->openControlStream(*ctx_);
    if (!stream_) {
      qWarning() << "ControlStreamWorker::run() - Stream is null, "
                    "emitting finished.";
      emit finished(QString("Failed to open stream: stream_ is null"));
      return;
    }
    qInfo() << "ControlStreamWorker::run() - Stream opened successfully.";
  } catch (const std::exception &e) {
    qCritical() << "ControlStreamWorker::run() - Exception opening stream: "
                << e.what();
    emit finished(QString("Failed to open stream: %1").arg(e.what()));
    return;
  }

  client::ServerMsg in;
  bool read_ok = true; // Assume read will be attempted
  qInfo() << "ControlStreamWorker::run() - Entering gRPC Read loop...";

  while (true) {
    if (quit_.load(std::memory_order_acquire)) {
      qInfo() << "ControlStreamWorker::run() - quit_ is true (checked "
                 "before Read), breaking.";
      read_ok = false; // Indicate loop should break due to quit
      break;
    }

    // Can be too noisy
    // qInfo() << "ControlStreamWorker::run() - Calling
    // stream_->Read(&in)...";

    read_ok = stream_->Read(&in); // This is the blocking call

    // Can be too noisy
    // qInfo() << "ControlStreamWorker::run() - stream_->Read(&in) returned:
    // " << read_ok
    //          << ". quit_ is " << quit_.load(std::memory_order_acquire);

    if (!read_ok) {
      qInfo() << "ControlStreamWorker::run() - stream_->Read() returned "
                 "false. Breaking loop.";
      break;
    }

    if (quit_.load(std::memory_order_acquire)) {
      qInfo() << "ControlStreamWorker::run() - quit_ is true (checked "
                 "after successful Read), breaking.";
      break;
    }
    emit serverMsg(in);
  }
  qInfo() << "ControlStreamWorker::run() - Exited gRPC Read loop. read_ok was "
          << read_ok;

  // Attempt to gracefully finish the stream if it wasn't an abrupt error
  // This also retrieves the final status from the server.
  grpc::Status status = grpc::Status::OK; // Default to OK
  if (stream_) {
    qInfo() << "ControlStreamWorker::run() - Calling stream_->Finish()...";
    status = stream_->Finish(); // This can block if server doesn't respond
                                // to WritesDone/cancel
    qInfo() << "ControlStreamWorker::run() - stream_->Finish() completed "
               "with status code: "
            << status.error_code()
            << ", message: " << QString::fromStdString(status.error_message());
  } else {
    qWarning() << "ControlStreamWorker::run() - Stream was null or reset, "
                  "skipping Finish().";
    if (quit_.load(std::memory_order_acquire)) {
      status = grpc::Status(grpc::StatusCode::CANCELLED,
                            "Stream shutdown by client");
    }
  }

  qInfo() << "ControlStreamWorker::run() - Emitting finished signal.";
  emit finished(QString("Stream closed: %1 (%2)")
                    .arg(status.ok()
                             ? "OK"
                             : QString::fromStdString(status.error_message()))
                    .arg(status.error_code()));
  qInfo() << "ControlStreamWorker::run() - Method finished.";
}

void ControlStreamWorker::start() {
  qInfo() << "ControlStreamWorker::start() CALLED from thread:"
          << QThread::currentThreadId();
  run();
}

void ControlStreamWorker::stop() {
  qInfo() << "ControlStreamWorker::stop() CALLED in thread:"
          << QThread::currentThreadId();
  if (quit_.exchange(true)) { // Atomically set to true and get old value
    qInfo() << "ControlStreamWorker::stop() - quit_ was already true. Stop "
               "already in progress.";
    return;
  }
  qInfo() << "ControlStreamWorker::stop() - quit_ flag is NOW true.";

  if (stream_) {
    qInfo() << "ControlStreamWorker::stop() - Calling stream_->WritesDone()...";
    if (!stream_->WritesDone()) { // Returns false if already finished or error
      qWarning() << "ControlStreamWorker::stop() - stream_->WritesDone() "
                    "indicated stream was already done or error.";
    } else {
      qInfo() << "ControlStreamWorker::stop() - stream_->WritesDone() "
                 "completed successfully.";
    }
  } else {
    qWarning() << "ControlStreamWorker::stop() - stream_ is null, cannot "
                  "call WritesDone().";
  }

  if (ctx_) {
    qInfo() << "ControlStreamWorker::stop() - Calling ctx_->TryCancel()...";
    ctx_->TryCancel();
    qInfo() << "ControlStreamWorker::stop() - ctx_->TryCancel() called.";
  } else {
    qWarning() << "ControlStreamWorker::stop() - ctx_ is null, cannot cancel.";
  }
}

bool ControlStreamWorker::isQuitFlagSet() const {
  return quit_.load(std::memory_order_acquire);
}

void ControlStreamWorker::send(const client::ClientMsg &msg) {
  qInfo() << "ControlStreamWorker::send() - ENTRY. Message payload_case:"
          << msg.payload_case();
  std::lock_guard<std::mutex> lk(txMu_);
  qInfo() << "ControlStreamWorker::send() - Lock acquired.";
  if (stream_) {
    qInfo() << "ControlStreamWorker::send() - About to write to stream...";
    bool ok = stream_->Write(msg, grpc::WriteOptions());
    qInfo() << "ControlStreamWorker::send() - Write completed. Success:" << ok;
  } else {
    qWarning()
        << "ControlStreamWorker::send() - stream_ is null! Cannot write.";
  }
  qInfo() << "ControlStreamWorker::send() - EXIT.";
}

} // namespace P2P
