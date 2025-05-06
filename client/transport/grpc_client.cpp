#include "transport/grpc_client.h" // Correct include path relative to 'client/'

#include <google/protobuf/empty.pb.h> // Include for Empty
#include <grpcpp/create_channel.h>
#include <stdexcept> // For std::runtime_error

namespace P2P {

GrpcClient::GrpcClient(const std::string& target_address) {
    channel_ =
        grpc::CreateChannel(target_address, grpc::InsecureChannelCredentials());
    if (!channel_) {
        throw std::runtime_error("Failed to create gRPC channel to " +
                                 target_address);
    }
    // Use the correct generated stub type: p2p::P2PTClient::NewStub
    stub_ = client::P2PTClient::NewStub(channel_);
    if (!stub_) {
        throw std::runtime_error("Failed to create gRPC stub");
    }
}

// Return the generated p2p::ServiceInfo message
client::ServiceInfo GrpcClient::getServiceInfo() {
    // Use google::protobuf::Empty for the request
    google::protobuf::Empty request;
    client::ServiceInfo response;
    grpc::ClientContext context;

    // Use the correct stub_ type
    grpc::Status status = stub_->GetServiceInfo(&context, request, &response);

    if (status.ok()) {
        return response; // Return the whole response message
    } else {
        throw std::runtime_error("GetServiceInfo RPC failed: " +
                                 status.error_message());
    }
}

// Use the generated p2p::ClientMsg and p2p::ServerMsg
std::unique_ptr<grpc::ClientReaderWriter<client::ClientMsg, client::ServerMsg>>
GrpcClient::openControlStream() {
    auto context = std::make_unique<grpc::ClientContext>();
    // Use the correct stub_ type
    return stub_->ControlStream(context.release());
}

// openControlStream with external context
std::unique_ptr<grpc::ClientReaderWriter<client::ClientMsg, client::ServerMsg>>
GrpcClient::openControlStream(grpc::ClientContext& ctx) {
    // caller owns ctx lifetime & cancellation
    return stub_->ControlStream(&ctx);
}

} // namespace P2P