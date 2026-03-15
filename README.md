# P2PTogether

**P2PTogether** is a decentralized, peer-to-peer (P2P) desktop application designed for synchronized video co-watching and real-time user interaction. It eliminates the need for central video distribution servers (relying only on bootstrap nodes for initial discovery) by allowing users to stream local video files directly to others while maintaining synchronized playback and offering tools for community interaction.

> [!WARNING]
> This project is in active development and remains in its early stages. It is **not yet production-ready**, so expect breaking changes and experimental features.

## Features

- **Decentralized P2P Streaming:** Streams local video files directly between peers using an embedded HLS server and `libp2p`-based networking.
- **Live Playback Synchronization:** Syncs player states across all peers (play, pause, seek, playback speed) to ensure everyone watches the same content simultaneously.
- **Role-Based Access Control (RBAC):** Flexible permission system with predefined roles (`Admin`, `Moderator`, `Streamer`, `Viewer`) to govern who can control playback, manage the video queue, or moderate the chat. You can add, remove, and modify roles, as well as assign them to users.
- **Media Queuing System:** Seamlessly switches between videos and different broadcasters in the queue.
- **Real-time Text Chat:** Built-in chat using GossipSub for snappy, decentralized communication.
- **Moderation Tools:** Kick, ban, message deletion, and dynamic role management to keep watch parties safe and organized.

## Architecture

The system is built on a two-process architecture offering strict separation of concerns:

1. **Application Daemon (Go Backend):**
   - Acts as the core P2P node.
   - Handles networking logic using [libp2p](https://libp2p.io/).
   - Manages the media pipeline (transcoding via FFmpeg, HLS segmentation, RingBuffer).
   - Governs session state, RBAC, and the video queue.
2. **Client Application (C++ / Qt Frontend):**
   - Provides a responsive, native Graphical User Interface (GUI) built with Qt.
   - Renders video efficiently using [libmpv](https://mpv.io/).
   - Communicates with the Daemon via **gRPC**.

## Dependencies

To build and run P2PTogether, you need the following dependencies installed on your system:

- [Go](https://golang.org/) (1.25.7)
- A modern C++ compiler (GCC, Clang, or MSVC)
- [FFmpeg](https://ffmpeg.org/) (must be available in the system PATH)
- [CMake](https://cmake.org/)
- [Qt 6](https://www.qt.io/)
- [mpv](https://mpv.io/), libmpv development headers
- [gRPC](https://grpc.io/)
- [Protocol Buffers](https://protobuf.dev/)

## Building the Project

The project uses CMake to orchestrate the build process for both the C++ frontend and the Go backend automatically. A `CMakePresets.json` is included for convenience with a Ninja Multi-Config generator.

**Available build configurations:** `Debug`, `Release`, `RelWithDebInfo`, `MinSizeRel`

Configure and build:

```bash
cmake --preset ninja-multi
cmake --build --preset Release
```

For subsequent builds, a single build command is sufficient:

```bash
cmake --build --preset Release
```

> [!NOTE]
> CMake will automatically invoke `go build` to compile the `peer_service` daemon and copy it alongside the `P2PTogether` executable in the selected configuration's output directory (e.g., `build/bin/Release/`).

## Usage

1. Launch the compiled C++ Client application.
2. The client will automatically start the background Go Daemon.
3. **Create a Session:** Start a new watch party to become the `Admin` (all permissions by default). Share the provided Invite Code with your friends.
4. **Join a Session:** Use an Invite Code to join an existing session. New joiners are granted the `Viewer` role by default.
5. **Add Media:** The `Admin` or users with appropriate permissions can add video files to the queue.

## License

This project is licensed under the **GNU General Public License v3.0 (GPL-3.0)**. See [LICENSE](LICENSE) for more information.

**Exceptions:**
The following files were adjusted from examples in the [`mpv-player/mpv-examples`](https://github.com/mpv-player/mpv-examples) repository and are in the public domain:

- [`client/player/qthelper.hpp`](client/player/qthelper.hpp) ([original](https://github.com/mpv-player/mpv-examples/blob/master/libmpv/common/qthelper.hpp))
- [`client/player/mpvwidget.h`](client/player/mpvwidget.h) ([original](https://github.com/mpv-player/mpv-examples/blob/master/libmpv/qt_opengl/mpvwidget.h))
- [`client/player/mpvwidget.cpp`](client/player/mpvwidget.cpp) ([original](https://github.com/mpv-player/mpv-examples/blob/master/libmpv/qt_opengl/mpvwidget.cpp))
