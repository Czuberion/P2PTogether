cmake_minimum_required(VERSION 3.16)
project(P2PTogether VERSION 0.1 LANGUAGES CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

include(CTest)

# If vcpkg toolchain is not used (e.g. Qt Kit overrides it), manually add vcpkg paths
if(WIN32 AND NOT CMAKE_TOOLCHAIN_FILE MATCHES "vcpkg")
    if(NOT DEFINED VCPKG_TARGET_TRIPLET)
        set(VCPKG_TARGET_TRIPLET "x64-mingw-dynamic" CACHE STRING "Vcpkg target triplet")
    endif()
    list(APPEND CMAKE_PREFIX_PATH "C:/vcpkg/installed/${VCPKG_TARGET_TRIPLET}")
    
    # Explicitly set the protoc executable path to ensure it is found
    set(_vcpkg_protoc "C:/vcpkg/installed/${VCPKG_TARGET_TRIPLET}/tools/protobuf/protoc.exe")
    if(EXISTS "${_vcpkg_protoc}")
        set(Protobuf_PROTOC_EXECUTABLE "${_vcpkg_protoc}" CACHE FILEPATH "Protobuf compiler" FORCE)
        message(STATUS "Forcing Protobuf_PROTOC_EXECUTABLE to: ${_vcpkg_protoc}")
    else()
        message(WARNING "Could not find protoc.exe at expected location: ${_vcpkg_protoc}")
    endif()

    message(STATUS "Manually added vcpkg prefix path: C:/vcpkg/installed/${VCPKG_TARGET_TRIPLET}")
endif()

find_package(Qt6 REQUIRED COMPONENTS Core Widgets OpenGLWidgets Network Test)
if(TARGET Qt6::Test)
    get_target_property(QT_TEST_LOC Qt6::Test LOCATION)
    message(STATUS "Qt6::Test LOCATION: ${QT_TEST_LOC}")
else()
    message(WARNING "Qt6::Test target not found!")
endif()
if(WIN32)
    find_package(Protobuf CONFIG REQUIRED)
    find_package(gRPC CONFIG REQUIRED)
else()
    find_package(Protobuf REQUIRED)
    find_package(gRPC REQUIRED)
endif()
find_package(Doxygen QUIET)

# --- Find MPV in a cross-platform way ----------------------------------------
find_package(PkgConfig QUIET)
if(PKG_CONFIG_FOUND)
    pkg_check_modules(PC_MPV QUIET mpv)
endif()

find_path(MPV_INCLUDE_DIR mpv/client.h
          HINTS ${PC_MPV_INCLUDE_DIRS} ${MPV_DIR} ENV MPV_DIR
          PATH_SUFFIXES include)

find_library(MPV_LIBRARY NAMES mpv-2 mpv libmpv
             HINTS ${PC_MPV_LIBRARY_DIRS} ${MPV_DIR} ENV MPV_DIR
             PATH_SUFFIXES lib lib64)

find_library(MPV_IMPORT_LIBRARY NAMES mpv-2 mpv libmpv
             HINTS ${PC_MPV_LIBRARY_DIRS} ${MPV_DIR} ENV MPV_DIR
             PATH_SUFFIXES lib lib64)

if (MPV_INCLUDE_DIR AND MPV_IMPORT_LIBRARY)
    message(STATUS "Found mpv import lib: ${MPV_IMPORT_LIBRARY}")

    if(WIN32)
        # locate the actual runtime DLL (same folder or ../bin)
        get_filename_component(_mpv_dir "${MPV_IMPORT_LIBRARY}" DIRECTORY)
        find_file(MPV_RUNTIME_DLL
                  NAMES libmpv-2.dll libmpv.dll mpv-2.dll
                  HINTS "${_mpv_dir}" "${_mpv_dir}/../bin" ${MPV_DIR} ENV MPV_DIR
                  PATH_SUFFIXES bin
                  NO_DEFAULT_PATH)
        if (NOT MPV_RUNTIME_DLL)
            message(FATAL_ERROR
                "Found mpv import lib at ${MPV_IMPORT_LIBRARY} but no runtime DLL.")
        endif()

        # create the imported target
        add_library(MPV::mpv SHARED IMPORTED)
        set_target_properties(MPV::mpv PROPERTIES
            INTERFACE_INCLUDE_DIRECTORIES "${MPV_INCLUDE_DIR}"
            IMPORTED_IMPLIB               "${MPV_IMPORT_LIBRARY}"
            IMPORTED_LOCATION             "${MPV_RUNTIME_DLL}")
    else()
        # On Linux/macOS, the import lib is the shared library
        add_library(MPV::mpv SHARED IMPORTED)
        set_target_properties(MPV::mpv PROPERTIES
            INTERFACE_INCLUDE_DIRECTORIES "${MPV_INCLUDE_DIR}"
            IMPORTED_LOCATION             "${MPV_IMPORT_LIBRARY}")
    endif()

    # # On Windows the linker also needs the import library (*.lib / *.dll.a)
    # if (WIN32)
    #     get_filename_component(_mpv_dir "${MPV_LIBRARY}" DIRECTORY)
    #     foreach(_cand
    #             "${_mpv_dir}/libmpv.dll.a"   # MinGW / clang (GNU mode)
    #             "${_mpv_dir}/mpv.lib")       # MSVC / clang-cl
    #         if (EXISTS "${_cand}")
    #             set_target_properties(MPV::mpv PROPERTIES
    #                 IMPORTED_IMPLIB "${_cand}")
    #             break()
    #         endif ()
    #     endforeach()
    # endif ()
else()
    message(FATAL_ERROR
        "Could not find libmpv. Please install it, or set MPV_DIR to the "
        "folder that contains include/mpv/client.h and the mpv DLL/LIB.")
endif()

# --- Windows-specific Protobuf/Clang Hotfix ---
if(WIN32)
    add_compile_definitions(_DISABLE_CONSTINIT_INITIALIZERS)
endif()

# Set all binary outputs to build/bin/$<CONFIG>
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/bin/$<CONFIG>)

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
    client/roles/role_store.cpp
    client/test/test_server.cpp
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
    client/utils/net.h
    client/roles/role_store.h
    client/roles/permissions.h
    client/test/test_server.h
)

# --- C++ gRPC/protobuf stub generation ---
# Use imported executable targets from gRPC/Protobuf packages (vcpkg-friendly)
if(TARGET gRPC::grpc_cpp_plugin)
  get_target_property(GRPC_CPP_PLUGIN gRPC::grpc_cpp_plugin LOCATION)
else()
  find_program(GRPC_CPP_PLUGIN grpc_cpp_plugin REQUIRED)
endif()

if(TARGET protobuf::protoc)
  get_target_property(PROTOC_EXECUTABLE protobuf::protoc LOCATION)
else()
  find_program(PROTOC_EXECUTABLE protoc REQUIRED)
endif()
set(PROTO_ROOT "${CMAKE_SOURCE_DIR}/proto")
set(CLIENT_PROTO "${PROTO_ROOT}/client.proto")
set(CPP_CLIENT_PROTO_OUT_DIR "${CMAKE_SOURCE_DIR}/client/proto")

file(GLOB_RECURSE ALL_PROTO_FILES CONFIGURE_DEPENDS
     "${PROTO_ROOT}/*.proto"
)

list(SORT ALL_PROTO_FILES)

# Build deterministic output lists from source .proto paths.
# This avoids re-configure requirements on fresh clones.
set(PROTO_REL_PATHS "")
set(PROTO_WITH_SERVICE_REL_PATHS "")
set(CPP_PROTO_SRCS "")
set(CPP_PROTO_HDRS "")
foreach(PROTO_FILE IN LISTS ALL_PROTO_FILES)
    file(RELATIVE_PATH PROTO_REL_PATH "${PROTO_ROOT}" "${PROTO_FILE}")
    list(APPEND PROTO_REL_PATHS "${PROTO_REL_PATH}")

    string(REGEX REPLACE "\\.proto$" "" PROTO_BASE_REL "${PROTO_REL_PATH}")
    list(APPEND CPP_PROTO_SRCS
        "${CPP_CLIENT_PROTO_OUT_DIR}/${PROTO_BASE_REL}.pb.cc"
        "${CPP_CLIENT_PROTO_OUT_DIR}/${PROTO_BASE_REL}.grpc.pb.cc")
    list(APPEND CPP_PROTO_HDRS
        "${CPP_CLIENT_PROTO_OUT_DIR}/${PROTO_BASE_REL}.pb.h"
        "${CPP_CLIENT_PROTO_OUT_DIR}/${PROTO_BASE_REL}.grpc.pb.h")

    file(READ "${PROTO_FILE}" PROTO_CONTENT)
    if(PROTO_CONTENT MATCHES "(^|[\\n\\r])[ \\t]*service[ \\t]+[A-Za-z_][A-Za-z0-9_]*")
        list(APPEND PROTO_WITH_SERVICE_REL_PATHS "${PROTO_REL_PATH}")
    endif()
endforeach()

add_custom_command(
    OUTPUT ${CPP_PROTO_SRCS} ${CPP_PROTO_HDRS}
  COMMAND ${CMAKE_COMMAND} -E make_directory ${CPP_CLIENT_PROTO_OUT_DIR}
  COMMAND ${PROTOC_EXECUTABLE}
    --plugin=protoc-gen-grpc=${GRPC_CPP_PLUGIN} # Specify the plugin path
    --cpp_out=${CPP_CLIENT_PROTO_OUT_DIR}
    --grpc_out=${CPP_CLIENT_PROTO_OUT_DIR}
    -I ${PROTO_ROOT}
    ${ALL_PROTO_FILES}
  DEPENDS ${ALL_PROTO_FILES} ${GRPC_CPP_PLUGIN}
  COMMENT "Generating C++ gRPC/protobuf code for client.proto"
  VERBATIM
)

list(APPEND SOURCES ${CPP_PROTO_SRCS})

add_executable(P2PTogether ${SOURCES} ${HEADERS})

# Include directories
target_include_directories(P2PTogether PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/client
    ${CPP_CLIENT_PROTO_OUT_DIR}
)

target_link_libraries(P2PTogether PRIVATE
    Qt6::Core
    Qt6::Widgets
    Qt6::OpenGLWidgets
    Qt6::Network
    MPV::mpv
    gRPC::grpc++
)

if(TARGET Qt6::Test)
    get_target_property(QT_TEST_LOC Qt6::Test LOCATION)
    message(STATUS "Forcing link of Qt6::Test library: ${QT_TEST_LOC}")
    target_link_libraries(P2PTogether PRIVATE ${QT_TEST_LOC})
endif()

# ──────────────────────────────────────────────────────────────────────────────
#  Post-build deploy step (only runs on Windows)
#     - copies all Qt runtime files with windeployqt
#     - drops the exact libmpv-2.dll we linked against next to the EXE
# ──────────────────────────────────────────────────────────────────────────────
if (WIN32)

    #
    # --- 1. Locate windeployqt ------------------------------------------------
    #
    # Qt’s own CMake config lives two folders deeper than the tool:
    #   Qt6_DIR  =  …/Qt/6.x.x/msvc2022_64/lib/cmake/Qt6
    #   windeployqt.exe  →  …/Qt/6.x.x/msvc2022_64/bin
    #
    get_filename_component(_qt_prefix "${Qt6_DIR}/../../.." REALPATH)
    find_program(QT_DEPLOY_TOOL
                 NAMES windeployqt windeployqt6
                 PATHS "${_qt_prefix}/bin"
                 NO_DEFAULT_PATH
                 REQUIRED)

    #
    # --- 2. Figure out the configuration name for windeployqt -----------------
    #
    # In multi-config generators (Visual Studio) the build type is decided
    # *when* we invoke MSBuild, not when CMake is run.  Generator expressions
    # hand the correct keyword (“debug” or “release”) to windeployqt.
    #
    set(_qt_conf $<$<CONFIG:Debug>:--debug>$<$<NOT:$<CONFIG:Debug>>:--release>)

    #
    # --- 3. Determine the mpv DLL that our IMPORTED target points at ----------
    #
    # $<TARGET_FILE:MPV::mpv> expands to the runtime file named in
    # IMPORTED_LOCATION, regardless of whether that’s “libmpv-2.dll” or
    # “C:/foo/bar/mpv-2.dll”.
    #
    # (no longer needed; we’ll reference the target directly)
    # set(_mpv_dll "$<TARGET_FILE:MPV::mpv>")

    #
    # --- 4. Add the post-build rule ------------------------------------------
    #
    # Get compiler runtime directory (System MinGW)
    get_filename_component(_compiler_dir "${CMAKE_CXX_COMPILER}" DIRECTORY)

    add_custom_command(TARGET P2PTogether POST_BUILD
        # 4a) run windeployqt – this pulls in all Qt dlls, plugins, ICU, etc.
        # NOTE: Removed --compiler-runtime to prevent copying incompatible Qt SDK runtimes.
        COMMAND "${QT_DEPLOY_TOOL}" ${_qt_conf} --verbose 1
                --no-compiler-runtime
                "$<TARGET_FILE:P2PTogether>"

        # 4b) Manually copy the CORRECT compiler runtime (from the compiler used to build)
        COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${_compiler_dir}/libstdc++-6.dll" "$<TARGET_FILE_DIR:P2PTogether>"
        COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${_compiler_dir}/libgcc_s_seh-1.dll" "$<TARGET_FILE_DIR:P2PTogether>"
        COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${_compiler_dir}/libwinpthread-1.dll" "$<TARGET_FILE_DIR:P2PTogether>"

        # 4c) copy the exact mpv runtime we linked against
        COMMAND "${CMAKE_COMMAND}" -E copy_if_different
                "$<TARGET_FILE:MPV::mpv>"
                "$<TARGET_FILE_DIR:P2PTogether>"

        COMMENT "Deploying Qt and libmpv runtime files…")

endif ()

# Needed for mpvwidget/qthelper
target_compile_definitions(P2PTogether PRIVATE QT_DEPRECATED_WARNINGS)
# target_compile_definitions(P2PTogether PRIVATE PROTOBUF_CONSTINIT=)
# target_compile_definitions(P2PTogether PRIVATE PROTOBUF_DISABLE_CONSTINIT)
# add_compile_definitions(PROTOBUF_DISABLE_CONSTINIT)

# Ensure MOC runs on headers that need it (like MpvWidget)
set_target_properties(P2PTogether PROPERTIES AUTOMOC ON)

# set(CMAKE_AUTOMOC_RELAXED_MODE ON) # Optional, can help with complex includes

# --- Portability Settings for RPATH (Linux/macOS) ---
# Set the RPATH to be relative to the executable's location.
# This is the key to making the binaries portable without installers.
set(CMAKE_INSTALL_RPATH "$ORIGIN") # For Linux
if(APPLE)
  set(CMAKE_INSTALL_RPATH "@executable_path") # For macOS
endif()

set(CMAKE_BUILD_WITH_INSTALL_RPATH ON)

# Doxygen documentation target
if(DOXYGEN_FOUND)
    message(STATUS "Doxygen found, 'doc' target will be available.")

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
else()
    message(STATUS "Doxygen not found, 'doc' target will be unavailable.")
endif()

# --- Go service build target ---
find_program(GO_EXECUTABLE go REQUIRED)


# -------------------------------------------------------------
#  Generate a throw‑away Go workspace at *source‑root*
#  ------------------------------------------------------------
set(GO_WORK_FILE "${CMAKE_SOURCE_DIR}/go.work")

# Dynamically find which modules actually exist so we don't break go.work
set(GO_WORK_MODULES "")
if(EXISTS "${CMAKE_SOURCE_DIR}/peer_service")
    list(APPEND GO_WORK_MODULES "${CMAKE_SOURCE_DIR}/peer_service")
endif()
if(EXISTS "${CMAKE_SOURCE_DIR}/tests/go")
    list(APPEND GO_WORK_MODULES "${CMAKE_SOURCE_DIR}/tests/go")
endif()

# We only create the workspace if we actually found modules
if(GO_WORK_MODULES)
    add_custom_command(
        OUTPUT  ${GO_WORK_FILE}
        # 1) nuke any stale workspace
        COMMAND ${CMAKE_COMMAND} -E rm -f ${GO_WORK_FILE}
        # 2) recreate it pointing at the discovered modules
        COMMAND ${GO_EXECUTABLE} work init ${GO_WORK_MODULES}
        # 3) keep it up‑to‑date if we ever change dependencies
        COMMAND ${GO_EXECUTABLE} work sync
        WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
        COMMENT "Regenerating temporary go.work workspace at repo root"
        VERBATIM)

    add_custom_target(go_workspace DEPENDS ${GO_WORK_FILE})
else()
    # Dummy target if no modules exist
    add_custom_target(go_workspace)
endif()

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
find_program(PROTOC_GEN_GO protoc-gen-go REQUIRED)
find_program(PROTOC_GEN_GO_GRPC protoc-gen-go-grpc REQUIRED)
set(GO_CLIENT_OUT_DIR "${CMAKE_SOURCE_DIR}/peer_service/proto")

set(GO_PROTO_SRCS "")
set(GO_GRPC_PROTO_SRCS "")
foreach(PROTO_REL_PATH IN LISTS PROTO_REL_PATHS)
    string(REGEX REPLACE "\\.proto$" "" PROTO_BASE_REL "${PROTO_REL_PATH}")
    list(APPEND GO_PROTO_SRCS "${GO_CLIENT_OUT_DIR}/${PROTO_BASE_REL}.pb.go")
endforeach()
foreach(PROTO_REL_PATH IN LISTS PROTO_WITH_SERVICE_REL_PATHS)
    string(REGEX REPLACE "\\.proto$" "" PROTO_BASE_REL "${PROTO_REL_PATH}")
    list(APPEND GO_GRPC_PROTO_SRCS "${GO_CLIENT_OUT_DIR}/${PROTO_BASE_REL}_grpc.pb.go")
endforeach()
set(GO_PROTO_OUTPUTS ${GO_PROTO_SRCS} ${GO_GRPC_PROTO_SRCS})

add_custom_command(
    OUTPUT ${GO_PROTO_OUTPUTS}
  COMMAND ${CMAKE_COMMAND} -E make_directory ${GO_CLIENT_OUT_DIR}
  COMMAND ${PROTOC_EXECUTABLE}
    --plugin=protoc-gen-go=${PROTOC_GEN_GO}
    --go_out=paths=source_relative:${GO_CLIENT_OUT_DIR}
    --plugin=protoc-gen-go-grpc=${PROTOC_GEN_GO_GRPC}
    --go-grpc_out=paths=source_relative:${GO_CLIENT_OUT_DIR}
    -I ${PROTO_ROOT}
    ${ALL_PROTO_FILES}
  DEPENDS ${ALL_PROTO_FILES}
  COMMENT "Generating Go code for *.proto"
  VERBATIM
)

add_custom_target(generate_go_client_proto ALL DEPENDS ${GO_PROTO_OUTPUTS})

set(GO_SERVICE_SRC ${CMAKE_SOURCE_DIR}/peer_service/main.go)
set(GO_SERVICE_DIR ${CMAKE_SOURCE_DIR}/peer_service)
# Build to a fixed staging location (Go has no Debug/Release distinction)
set(GO_SERVICE_BIN
    "${CMAKE_BINARY_DIR}/bin/peer_service${CMAKE_EXECUTABLE_SUFFIX}")

# Ensure bin directory exists
file(MAKE_DIRECTORY ${CMAKE_BINARY_DIR}/bin)

file(GLOB_RECURSE GO_SERVICE_SRCS CONFIGURE_DEPENDS
     ${GO_SERVICE_DIR}/*.go)

add_custom_command(
    OUTPUT "${GO_SERVICE_BIN}"
    DEPENDS ${GO_SERVICE_SRCS}
            ${GO_WORK_FILE}
            ${GO_PROTO_OUTPUTS}
    COMMAND ${GO_EXECUTABLE} mod tidy
    COMMAND ${GO_EXECUTABLE} build
            -o "${GO_SERVICE_BIN}"
            "${GO_SERVICE_SRC}"
    WORKING_DIRECTORY "${GO_SERVICE_DIR}"
    COMMENT "Building Go peer_service - ${GO_SERVICE_BIN}"
    VERBATIM
)

add_custom_target(peer_service ALL DEPENDS "${GO_SERVICE_BIN}")
add_dependencies(peer_service generate_go_client_proto)

# Stage Go binary alongside the C++ executable in each config's output directory.
# Keep this as a dedicated dependency so it can run even when P2PTogether
# itself does not relink. A stamp file keeps it incremental.
set(PEER_SERVICE_STAGE_STAMP "${CMAKE_BINARY_DIR}/peer_service.stage.stamp")
set(PEER_SERVICE_STAGE_DIRS "")
if(CMAKE_CONFIGURATION_TYPES)
    foreach(cfg IN LISTS CMAKE_CONFIGURATION_TYPES)
        list(APPEND PEER_SERVICE_STAGE_DIRS "${CMAKE_BINARY_DIR}/bin/${cfg}")
    endforeach()
elseif(CMAKE_BUILD_TYPE)
    list(APPEND PEER_SERVICE_STAGE_DIRS "${CMAKE_BINARY_DIR}/bin/${CMAKE_BUILD_TYPE}")
else()
    list(APPEND PEER_SERVICE_STAGE_DIRS "${CMAKE_BINARY_DIR}/bin")
endif()

set(PEER_SERVICE_STAGE_COMMANDS "")
foreach(stage_dir IN LISTS PEER_SERVICE_STAGE_DIRS)
    list(APPEND PEER_SERVICE_STAGE_COMMANDS
        COMMAND ${CMAKE_COMMAND} -E make_directory "${stage_dir}"
        COMMAND ${CMAKE_COMMAND} -E copy_if_different
                "${GO_SERVICE_BIN}"
                "${stage_dir}/peer_service${CMAKE_EXECUTABLE_SUFFIX}")
endforeach()

add_custom_command(
    OUTPUT "${PEER_SERVICE_STAGE_STAMP}"
    ${PEER_SERVICE_STAGE_COMMANDS}
    COMMAND ${CMAKE_COMMAND} -E touch "${PEER_SERVICE_STAGE_STAMP}"
    DEPENDS "${GO_SERVICE_BIN}"
    COMMENT "Staging peer_service into runtime output directories"
    VERBATIM
)

add_custom_target(stage_peer_service_for_client DEPENDS "${PEER_SERVICE_STAGE_STAMP}")
add_dependencies(stage_peer_service_for_client peer_service)

# --- FFMPEG Handling ---
# Find the ffmpeg executable and copy it next to our other binaries post-build
find_program(FFMPEG_EXECUTABLE ffmpeg HINTS ENV FFMPEG_PATH)
if(FFMPEG_EXECUTABLE)
    # On Windows, package managers like Chocolatey use shims. We need to find the real executable.

    message(STATUS "Resolved ffmpeg real path: ${FFMPEG_EXECUTABLE}")

    add_custom_command(TARGET P2PTogether POST_BUILD
        COMMAND ${CMAKE_COMMAND} -E copy_if_different
                "${FFMPEG_EXECUTABLE}"
                "$<TARGET_FILE_DIR:P2PTogether>"
        COMMENT "Copying ffmpeg executable to output directory")
else()
    message(WARNING "ffmpeg executable not found. It will not be bundled with the application. The Go service will fail if ffmpeg is not in PATH at runtime.")
endif()

find_program(FFPROBE_EXECUTABLE ffprobe HINTS ENV FFMPEG_PATH)
if(FFPROBE_EXECUTABLE)
    # On Windows, package managers like Chocolatey use shims. We need to find the real executable.

    message(STATUS "Resolved ffprobe real path: ${FFPROBE_EXECUTABLE}")

    add_custom_command(TARGET P2PTogether POST_BUILD
        COMMAND ${CMAKE_COMMAND} -E copy_if_different
                "${FFPROBE_EXECUTABLE}"
                "$<TARGET_FILE_DIR:P2PTogether>"
        COMMENT "Copying ffprobe executable to output directory")
else()
    message(WARNING "ffprobe executable not found. It will not be bundled with the application. The Go service will fail if ffprobe is not in PATH at runtime.")
endif()

# Ensure proto generation before build
add_custom_target(generate_cpp_client_proto ALL DEPENDS ${CPP_PROTO_SRCS} ${CPP_PROTO_HDRS})
add_dependencies(P2PTogether generate_cpp_client_proto)
add_dependencies(P2PTogether stage_peer_service_for_client)