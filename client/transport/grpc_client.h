#pragma once

#include <grpcpp/grpcpp.h>
#include <memory>
#include <string>

#include "client.grpc.pb.h"           // Correct include path
#include <google/protobuf/empty.pb.h> // Include for Empty

namespace P2P {

class GrpcClient {
public:
    // Constructor takes the server address
    explicit GrpcClient(const std::string& target_address = "127.0.0.1:0");

    // Calls the GetServiceInfo RPC
    // Returns the generated p2p::ServiceInfo message
    client::ServiceInfo getServiceInfo();

    // Opens the bidirectional control stream
    // Uses the generated p2p::ClientMsg and p2p::ServerMsg
    std::unique_ptr<
        grpc::ClientReaderWriter<client::ClientMsg, client::ServerMsg>>
    openControlStream();

    // Opens the bidirectional control stream.
    // Lets the caller provide (and later cancel) the context
    std::unique_ptr<
        grpc::ClientReaderWriter<client::ClientMsg, client::ServerMsg>>
    openControlStream(grpc::ClientContext& ctx);

private:
    std::shared_ptr<grpc::Channel> channel_;
    // Use the correct generated stub type: p2p::P2PTClient::Stub
    std::unique_ptr<client::P2PTClient::Stub> stub_;
};

} // namespace P2P
