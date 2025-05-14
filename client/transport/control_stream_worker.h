#pragma once

#include <QObject>
#include <atomic>
#include <grpcpp/client_context.h>

#include "transport/grpc_client.h"

namespace P2P {

/*!
 * \brief Runs inside its own QThread, blocking on ServerMsg → emits Qt signals
 * when something arrives.
 *
 * Thread-safe `send()` lets any GUI object push ClientMsg onto the stream (the
 * “consumer → producer” direction).
 */
class ControlStreamWorker : public QObject {
    Q_OBJECT
public:
    explicit ControlStreamWorker(std::unique_ptr<GrpcClient> client,
                                 QObject* parent = nullptr);
    ~ControlStreamWorker() override;

    void stop();
    bool isQuitFlagSet() const;

public Q_SLOTS:
    //! enqueue a ClientMsg to be written on the stream
    void send(const client::ClientMsg& msg);
    //! kicks off the blocking read-loop
    void start();

Q_SIGNALS:
    //! emitted on **any** ServerMsg (slot in GUI decides what to do)
    void serverMsg(const client::ServerMsg& msg);

    //! emitted if the stream breaks or worker stops itself
    void finished(const QString& reason);

private:
    void run(); // blocking loop
    std::atomic<bool> quit_ {false};
    std::unique_ptr<grpc::ClientContext> ctx_;

    std::unique_ptr<GrpcClient> client_;
    std::unique_ptr<
        grpc::ClientReaderWriter<client::ClientMsg, client::ServerMsg>>
        stream_;
    std::mutex txMu_; // protects *stream_ writes
};

} // namespace P2P
