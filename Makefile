QT_VERSION := $(shell if [ -d "$(HOME)/Qt/6.10.2/macos" ]; then echo "6.10.2"; elif [ -d "$(HOME)/Qt/6.10.1/macos" ]; then echo "6.10.1"; elif [ -d "$(HOME)/Qt/6.10.0/macos" ]; then echo "6.10.0"; else echo ""; fi)
QT_PATH := $(HOME)/Qt/$(QT_VERSION)/macos
CMAKE := $(HOME)/Qt/Tools/CMake/CMake.app/Contents/bin/cmake

.PHONY: all configure build run clean

all: build

configure:
	mkdir -p build
	cd build && $(CMAKE) -G "Unix Makefiles" -DCMAKE_PREFIX_PATH="$(QT_PATH)" ..

build: configure
	cd build && $(MAKE) -j8

run: build
	open build/tdx_companion.app

clean:
	rm -rf build
