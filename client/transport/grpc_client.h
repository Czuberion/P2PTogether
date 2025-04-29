#ifndef P2P_TRANSPORT_GRPC_CLIENT_H
#define P2P_TRANSPORT_GRPC_CLIENT_H

#include <grpcpp/grpcpp.h>
#include <memory>
#include <string>

#include "client.grpc.pb.h"           // Correct include path
#include <google/protobuf/empty.pb.h> // Include for Empty

namespace P2P {

// Use the generated ServiceInfo message directly
using p2p::ServiceInfo;

class GrpcClient {
public:
    // Constructor takes the server address (e.g., "127.0.0.1:8268")
    explicit GrpcClient(const std::string& target_address = "127.0.0.1:8268");

    // Calls the GetServiceInfo RPC
    // Returns the generated p2p::ServiceInfo message
    p2p::ServiceInfo getServiceInfo();

    // Opens the bidirectional control stream
    // Uses the generated p2p::ClientMsg and p2p::ServerMsg
    std::unique_ptr<grpc::ClientReaderWriter<p2p::ClientMsg, p2p::ServerMsg>>
    openControlStream();

private:
    std::shared_ptr<grpc::Channel> channel_;
    // Use the correct generated stub type: p2p::P2PTClient::Stub
    std::unique_ptr<p2p::P2PTClient::Stub> stub_;
};

} // namespace P2P

#endif // P2P_TRANSPORT_GRPC_CLIENT_H