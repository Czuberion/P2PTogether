cmake_minimum_required(VERSION 3.16)
project(P2PTogether VERSION 0.1 LANGUAGES CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(Qt6 REQUIRED COMPONENTS Widgets OpenGLWidgets)
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
)

add_executable(P2PTogether ${SOURCES} ${HEADERS})

# Include directories
target_include_directories(P2PTogether PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/client
    ${MPV_INCLUDE_DIRS}       # Add mpv include directory
)

# Link libraries
target_link_libraries(P2PTogether PRIVATE
    Qt6::Widgets
    Qt6::OpenGLWidgets      # Link against OpenGLWidgets
    PkgConfig::MPV          # Link against mpv using the imported target
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
find_program(PROTOC_EXECUTABLE protoc REQUIRED)
find_program(GO_EXECUTABLE go REQUIRED)

set(CLIENT_PROTO "${CMAKE_SOURCE_DIR}/proto/client.proto")
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