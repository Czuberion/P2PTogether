#include "transport/grpc_client.h" // Correct include path relative to 'client/'

#include <grpcpp/create_channel.h>
#include <stdexcept> // For std::runtime_error
#include <google/protobuf/empty.pb.h> // Include for Empty

namespace P2P {

GrpcClient::GrpcClient(const std::string& target_address) {
    channel_ = grpc::CreateChannel(target_address, grpc::InsecureChannelCredentials());
    if (!channel_) {
        throw std::runtime_error("Failed to create gRPC channel to " + target_address);
    }
    // Use the correct generated stub type: p2p::P2PTClient::NewStub
    stub_ = p2p::P2PTClient::NewStub(channel_);
    if (!stub_) {
        throw std::runtime_error("Failed to create gRPC stub");
    }
}

// Return the generated p2p::ServiceInfo message
p2p::ServiceInfo GrpcClient::getServiceInfo() {
    // Use google::protobuf::Empty for the request
    google::protobuf::Empty request;
    p2p::ServiceInfo response;
    grpc::ClientContext context;

    // Use the correct stub_ type
    grpc::Status status = stub_->GetServiceInfo(&context, request, &response);

    if (status.ok()) {
        return response; // Return the whole response message
    } else {
        throw std::runtime_error("GetServiceInfo RPC failed: " + status.error_message());
    }
}

// Use the generated p2p::ClientMsg and p2p::ServerMsg
std::unique_ptr<grpc::ClientReaderWriter<p2p::ClientMsg, p2p::ServerMsg>> GrpcClient::openControlStream() {
    auto context = std::make_unique<grpc::ClientContext>();
    // Use the correct stub_ type
    return stub_->ControlStream(context.release());
}

} // namespace P2P