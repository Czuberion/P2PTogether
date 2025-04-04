cmake_minimum_required(VERSION 3.16)
project(P2PTogether VERSION 0.1 LANGUAGES CXX)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(Qt6 REQUIRED COMPONENTS Widgets OpenGLWidgets)
find_package(PkgConfig REQUIRED)
pkg_check_modules(MPV REQUIRED IMPORTED_TARGET mpv)

set(SOURCES
    src/main.cpp
    src/p2p/peer.cpp
    src/gui/app.cpp
    src/gui/menus.cpp
    src/gui/right_panel.cpp
    src/gui/video_panel.cpp
    src/player/mpvwidget.cpp
    src/player/mpv_manager.cpp
)

set(HEADERS
    src/p2p/peer.h
    src/gui/app.h
    src/gui/menus.h
    src/gui/right_panel.h
    src/gui/video_panel.h
    src/player/mpvwidget.h
    src/player/qthelper.hpp
    src/player/mpv_manager.h
)

add_executable(P2PTogether ${SOURCES} ${HEADERS})

# Include directories
target_include_directories(P2PTogether PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/src
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