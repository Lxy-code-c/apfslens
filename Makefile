GO ?= go
GOTOOLCHAIN = go1.27.1
VER ?= 2026Y11M26D-4
WIN_LDFLAGS = -H windowsgui -s -w
UI_LDFLAGS  = -s -w

.PHONY: all windows linux darwin native test vet package clean

all: windows linux darwin native

# ---------------------------------------------------------------------------
# Windows（图形版，三架构；无终端窗口）
# ---------------------------------------------------------------------------
windows:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../udisk-tool-x64.exe .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=386   $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../udisk-tool-x86.exe .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../udisk-tool-arm64.exe .

# ---------------------------------------------------------------------------
# Linux（图形版，预览壳；amd64 / arm64）
# ---------------------------------------------------------------------------
linux:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(UI_LDFLAGS)" -o ../udisk-tool-linux-x64 .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(UI_LDFLAGS)" -o ../udisk-tool-linux-arm64 .

# ---------------------------------------------------------------------------
# macOS（图形版，预览壳；Apple Silicon / Intel）。.app 由 packaging 脚本打包。
# ---------------------------------------------------------------------------
darwin:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "$(UI_LDFLAGS)" -o ../udisk-tool-macos-arm64 .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=darwin GOARCH=amd64 $(GO) build -ldflags "$(UI_LDFLAGS)" -o ../udisk-tool-macos-x64 .

# Windows 原生辅助模块（C++ 搬运 / C 分区核对）
native:
	x86_64-w64-mingw32-g++-posix -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o udisk_native.exe native/native.cpp
	i686-w64-mingw32-g++-posix  -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o udisk_native-x86.exe native/native.cpp
	x86_64-w64-mingw32-gcc-posix -O2 -static -o udisk_probe.exe native/probe.c
	i686-w64-mingw32-gcc-posix  -O2 -static -o udisk_probe-x86.exe native/probe.c

test:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(GO) test -count=1 .

vet:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows $(GO) vet ./...

package: darwin
	bash packaging/macos-app.sh

clean:
	rm -f *.exe udisk-tool-linux-* udisk-tool-macos-*
