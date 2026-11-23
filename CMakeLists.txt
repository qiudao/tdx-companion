cmake_minimum_required(VERSION 3.16)

project(tdx_companion VERSION 0.1 LANGUAGES CXX)

set(CMAKE_AUTOUIC ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# macOS Tahoe (26) SDK C++ standard library header fix
if(APPLE AND CMAKE_OSX_SYSROOT)
    set(SDK_CXX_INCLUDE "${CMAKE_OSX_SYSROOT}/usr/include/c++/v1")
    if(EXISTS "${SDK_CXX_INCLUDE}" AND NOT EXISTS "/Library/Developer/CommandLineTools/usr/include/c++/v1/type_traits")
        add_compile_options(-isystem "${SDK_CXX_INCLUDE}")
    endif()
endif()

find_package(QT NAMES Qt6 Qt5 REQUIRED COMPONENTS Widgets Network)
find_package(Qt${QT_VERSION_MAJOR} REQUIRED COMPONENTS Widgets Network)

set(PROJECT_SOURCES
    main.cpp
    tdxbridge.h
    tdxbridge.cpp
    hudoverlay.h
    hudoverlay.cpp
    mainwindow.h
    mainwindow.cpp
)

if(${QT_VERSION_MAJOR} GREATER_EQUAL 6)
    qt_add_executable(tdx_companion MANUAL_FINALIZATION ${PROJECT_SOURCES})
else()
    add_executable(tdx_companion ${PROJECT_SOURCES})
endif()

target_link_libraries(tdx_companion PRIVATE
    Qt${QT_VERSION_MAJOR}::Widgets
    Qt${QT_VERSION_MAJOR}::Network
    iconv
    sqlite3
)

set_target_properties(tdx_companion PROPERTIES
    MACOSX_BUNDLE TRUE
    MACOSX_BUNDLE_BUNDLE_VERSION ${PROJECT_VERSION}
    MACOSX_BUNDLE_SHORT_VERSION_STRING ${PROJECT_VERSION_MAJOR}.${PROJECT_VERSION_MINOR}
)

if(QT_VERSION_MAJOR EQUAL 6)
    qt_finalize_executable(tdx_companion)
endif()
