QT_VERSION := $(shell if [ -d "$(HOME)/Qt/6.10.2/macos" ]; then echo "6.10.2"; elif [ -d "$(HOME)/Qt/6.10.1/macos" ]; then echo "6.10.1"; elif [ -d "$(HOME)/Qt/6.10.0/macos" ]; then echo "6.10.0"; else echo ""; fi)
QT_PATH := $(HOME)/Qt/$(QT_VERSION)/macos
CMAKE := $(HOME)/Qt/Tools/CMake/CMake.app/Contents/bin/cmake

.PHONY: all configure build run clean cli cli-install trade-agent hub-deploy

all: build

configure:
	mkdir -p build
	cd build && $(CMAKE) -G "Unix Makefiles" -DCMAKE_PREFIX_PATH="$(QT_PATH)" ..

build: configure
	cd build && $(MAKE) -j8

run: build
	open build/tdx_companion.app

cli:
	cd tools/tdx-cli && go build -ldflags="-s -w" -o tdx-cli .

cli-install: cli
	mkdir -p "$(HOME)/.local/bin"
	install -m 755 tools/tdx-cli/tdx-cli "$(HOME)/.local/bin/tdx-cli"

trade-agent:
	cd trade-agent && GOOS=windows GOARCH=arm64 go build -ldflags="-H=windowsgui -s -w" -o trade-agent.exe .

vision-ocr:
	swiftc -O tools/vision-ocr/main.swift -o tools/vision-ocr/vision-ocr -framework Vision -framework AppKit
	mkdir -p "$(HOME)/.local/bin"
	install -m 755 tools/vision-ocr/vision-ocr "$(HOME)/.local/bin/vision-ocr"

hub-deploy:
	cd tools/tdx-hub && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o tdx-hub-linux-amd64 .
	scp -q tools/tdx-hub/tdx-hub-linux-amd64 tengxun_kanglei:/tmp/tdx-hub
	rm -f tools/tdx-hub/tdx-hub-linux-amd64
	ssh tengxun_kanglei 'sudo mv /tmp/tdx-hub /opt/tdx-hub/tdx-hub && sudo chmod +x /opt/tdx-hub/tdx-hub && sudo systemctl restart tdx-hub.service && sudo systemctl is-active tdx-hub.service'

clean:
	rm -rf build tools/tdx-cli/tdx-cli
