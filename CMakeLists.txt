cmake_minimum_required(VERSION 3.16)
project(P2PTogether VERSION 0.1 LANGUAGES CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

include(CTest)

find_package(Qt6 REQUIRED COMPONENTS Core Widgets OpenGLWidgets)
find_package(PkgConfig REQUIRED)
pkg_check_modules(MPV REQUIRED IMPORTED_TARGET mpv)

# Set all binary outputs to build/bin
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/bin)

set(SOURCES
    client/main.cpp
    client/p2p/peer.cpp
    client/gui/app.cpp
    client/gui/menus.cpp
    client/gui/right_panel.cpp
    client/gui/video_panel.cpp
    client/player/mpvwidget.cpp
    client/player/mpv_manager.cpp
    client/transport/grpc_client.cpp
    client/transport/control_stream_worker.cpp
)

set(HEADERS
    client/p2p/peer.h
    client/gui/app.h
    client/gui/menus.h
    client/gui/right_panel.h
    client/gui/video_panel.h
    client/player/mpvwidget.h
    client/player/qthelper.hpp
    client/player/mpv_manager.h
    client/transport/grpc_client.h
    client/transport/control_stream_worker.h
)

# --- C++ gRPC/protobuf stub generation ---
find_program(GRPC_CPP_PLUGIN grpc_cpp_plugin REQUIRED) # Correct executable name
find_program(PROTOC_EXECUTABLE protoc REQUIRED)
set(CLIENT_PROTO "${CMAKE_SOURCE_DIR}/proto/client.proto")
set(CPP_CLIENT_PROTO_OUT_DIR "${CMAKE_SOURCE_DIR}/client/proto")

add_custom_command(
  OUTPUT ${CPP_CLIENT_PROTO_OUT_DIR}/client.pb.cc ${CPP_CLIENT_PROTO_OUT_DIR}/client.grpc.pb.cc
  COMMAND ${CMAKE_COMMAND} -E make_directory ${CPP_CLIENT_PROTO_OUT_DIR}
  COMMAND ${PROTOC_EXECUTABLE}
    --plugin=protoc-gen-grpc=${GRPC_CPP_PLUGIN} # Specify the plugin path
    --cpp_out=${CPP_CLIENT_PROTO_OUT_DIR}
    --grpc_out=${CPP_CLIENT_PROTO_OUT_DIR}
    -I ${CMAKE_SOURCE_DIR}/proto
    ${CLIENT_PROTO}
  DEPENDS ${CLIENT_PROTO} ${GRPC_CPP_PLUGIN}
  COMMENT "Generating C++ gRPC/protobuf code for client.proto"
  VERBATIM
)

# Add generated sources to build
list(APPEND SOURCES
  ${CPP_CLIENT_PROTO_OUT_DIR}/client.pb.cc
  ${CPP_CLIENT_PROTO_OUT_DIR}/client.grpc.pb.cc
)

add_executable(P2PTogether ${SOURCES} ${HEADERS})

# Include directories
target_include_directories(P2PTogether PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/client
    ${MPV_INCLUDE_DIRS}       # Add mpv include directory
    ${CPP_CLIENT_PROTO_OUT_DIR}
    ${Protobuf_INCLUDE_DIRS}
    ${gRPC_INCLUDE_DIRS}
)

# Link libraries
find_package(Protobuf REQUIRED)
find_package(gRPC REQUIRED)

if(NOT gRPC_FOUND)
  message(FATAL_ERROR "gRPC not found!")
endif()
if(NOT Protobuf_FOUND)
  message(FATAL_ERROR "Protobuf not found!")
endif()

target_link_libraries(P2PTogether PRIVATE
    Qt6::Core
    Qt6::Widgets
    Qt6::OpenGLWidgets
    PkgConfig::MPV
    gRPC::grpc++
    protobuf # Link protobuf directly by name
)

# Needed for mpvwidget/qthelper
target_compile_definitions(P2PTogether PRIVATE QT_DEPRECATED_WARNINGS)

# Ensure MOC runs on headers that need it (like MpvWidget)
set_target_properties(P2PTogether PROPERTIES AUTOMOC ON)

# set(CMAKE_AUTOMOC_RELAXED_MODE ON) # Optional, can help with complex includes

# Doxygen documentation target
find_package(Doxygen REQUIRED)

# Where to put generated docs
set(DOXYGEN_OUT       ${CMAKE_BINARY_DIR}/docs/doxygen)

# Input Doxyfile and staged copy for configure_file
set(DOXYFILE_IN       ${CMAKE_SOURCE_DIR}/Doxyfile)
set(DOXYFILE_STAGED   ${CMAKE_BINARY_DIR}/Doxyfile)

# Replace @CMAKE_SOURCE_DIR@ and @CMAKE_BINARY_DIR@ in your Doxyfile
configure_file(
  ${DOXYFILE_IN}
  ${DOXYFILE_STAGED}
  @ONLY
)

# Custom target to build docs
add_custom_target(doc
  # ensure the output directory exists
  COMMAND ${CMAKE_COMMAND} -E make_directory ${DOXYGEN_OUT}
  # run doxygen
  COMMAND ${DOXYGEN_EXECUTABLE} ${DOXYFILE_STAGED}
  WORKING_DIRECTORY ${CMAKE_BINARY_DIR}
  COMMENT "⏳ Generating Doxygen docs in ${DOXYGEN_OUT}"
  VERBATIM
)

# --- Go service build target ---
find_program(GO_EXECUTABLE go REQUIRED)

# -------------------------------------------------------------
#  Generate a throw‑away Go workspace at *source‑root*
#  ------------------------------------------------------------
set(GO_WORK_FILE "${CMAKE_SOURCE_DIR}/go.work")

add_custom_command(
    OUTPUT  ${GO_WORK_FILE}
    # 1) nuke any stale workspace
    COMMAND ${CMAKE_COMMAND} -E rm -f ${GO_WORK_FILE}
    # 2) recreate it pointing at our two modules
    COMMAND ${GO_EXECUTABLE} work init
            "${CMAKE_SOURCE_DIR}/peer_service"
            "${CMAKE_SOURCE_DIR}/tests/go"
    # 3) keep it up‑to‑date if we ever change dependencies
    COMMAND ${GO_EXECUTABLE} work sync
    WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
    COMMENT "Regenerating temporary go.work workspace at repo root"
    VERBATIM)

add_custom_target(go_workspace ALL DEPENDS ${GO_WORK_FILE})

# ---------------- Go vet / unit tests ----------------
enable_testing()

# --- All Go packages we want to analyse / test ---
set(GO_PKG_PATTERNS ./peer_service/...)

# ---- Custom targets that the build graph can depend on ----
add_custom_target(go_vet
    COMMAND ${GO_EXECUTABLE} vet ${GO_PKG_PATTERNS}
    WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
    DEPENDS go_workspace generate_go_client_proto
    COMMENT "Running go vet ${GO_PKG_PATTERNS}")

add_custom_target(go_unit_tests
    COMMAND ${GO_EXECUTABLE} test ${GO_PKG_PATTERNS}
    WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
    DEPENDS go_workspace generate_go_client_proto
    COMMENT "Running go test ${GO_PKG_PATTERNS}")

# ---- register the same commands with CTest (optional) ----
add_test(NAME go_vet
         COMMAND ${GO_EXECUTABLE} vet ${GO_PKG_PATTERNS}
         WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})

add_test(NAME go_unit_tests
         COMMAND ${GO_EXECUTABLE} test ${GO_PKG_PATTERNS}
         WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})

# Make sure proto generation & workspace exist before tests
add_dependencies(go_unit_tests go_workspace generate_go_client_proto)
add_dependencies(go_vet       go_workspace generate_go_client_proto)

# --- Go client proto generation ---
set(GO_CLIENT_OUT_DIR "${CMAKE_SOURCE_DIR}/peer_service/proto/client")

add_custom_command(
  OUTPUT ${GO_CLIENT_OUT_DIR}/client.pb.go ${GO_CLIENT_OUT_DIR}/client_grpc.pb.go
  COMMAND ${CMAKE_COMMAND} -E make_directory ${GO_CLIENT_OUT_DIR}
  COMMAND ${PROTOC_EXECUTABLE}
    --go_out=paths=source_relative:${GO_CLIENT_OUT_DIR}
    --go-grpc_out=paths=source_relative:${GO_CLIENT_OUT_DIR}
    -I ${CMAKE_SOURCE_DIR}/proto
    ${CLIENT_PROTO}
  DEPENDS ${CLIENT_PROTO}
  COMMENT "Generating Go code for client.proto"
  VERBATIM
)

add_custom_target(generate_go_client_proto ALL DEPENDS ${GO_CLIENT_OUT_DIR}/client.pb.go ${GO_CLIENT_OUT_DIR}/client_grpc.pb.go)

set(GO_SERVICE_SRC ${CMAKE_SOURCE_DIR}/peer_service/main.go)
set(GO_SERVICE_DIR ${CMAKE_SOURCE_DIR}/peer_service)
set(GO_SERVICE_BIN ${CMAKE_BINARY_DIR}/bin/peer_service)
set(GO_MOD_FILE ${GO_SERVICE_DIR}/go.mod)

# Ensure bin directory exists
file(MAKE_DIRECTORY ${CMAKE_BINARY_DIR}/bin)

add_custom_command(
  OUTPUT ${GO_MOD_FILE}
  COMMAND ${GO_EXECUTABLE} mod init p2ptogether/peer_service || exit 0
  WORKING_DIRECTORY ${GO_SERVICE_DIR}
  COMMENT "Initializing Go module for peer_service (if needed)"
  VERBATIM
)

add_custom_command(
  OUTPUT ${GO_SERVICE_BIN}
  DEPENDS ${GO_SERVICE_SRC} ${GO_MOD_FILE} generate_go_client_proto
  COMMAND ${GO_EXECUTABLE} mod tidy
  COMMAND ${GO_EXECUTABLE} build -o ${GO_SERVICE_BIN} ${GO_SERVICE_SRC}
  WORKING_DIRECTORY ${GO_SERVICE_DIR}
  COMMENT "Building Go peer service"
  VERBATIM
)

add_custom_target(peer_service ALL DEPENDS ${GO_SERVICE_BIN})

# Ensure proto generation before build
add_custom_target(generate_cpp_client_proto ALL DEPENDS ${CPP_CLIENT_PROTO_OUT_DIR}/client.pb.cc ${CPP_CLIENT_PROTO_OUT_DIR}/client.grpc.pb.cc)
add_dependencies(P2PTogether generate_cpp_client_proto)