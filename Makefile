GO ?= go
GOTOOLCHAIN = go1.27.1
VER ?= 2026Y11M26D-4
WIN_LDFLAGS = -H windowsgui -s -w
UI_LDFLAGS  = -s -w
CROSS_FLAGS = CGO_ENABLED=0

.PHONY: all windows linux darwin native test vet package icons clean

all: windows linux darwin native

# ---------------------------------------------------------------------------
# Windows（WebView2 原生窗口，三架构，无终端；图标由 src/rsrc_windows_*.syso 嵌入）
# ---------------------------------------------------------------------------
windows:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../build/apfslens-x64.exe .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=386   $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../build/apfslens-x86.exe .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../build/apfslens-arm64.exe .

# ---------------------------------------------------------------------------
# Linux（图形版，纯 Go 无 cgo；系统浏览器作窗口，只监听 127.0.0.1）
# ---------------------------------------------------------------------------
linux:
	cd src/cross && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(CROSS_FLAGS) GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../build/apfslens-linux-x64 .
	cd src/cross && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(CROSS_FLAGS) GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../build/apfslens-linux-arm64 .

# ---------------------------------------------------------------------------
# macOS（同上；.app 由 packaging/macos-app.sh 打包，图标已入库）
# ---------------------------------------------------------------------------
darwin:
	cd src/cross && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(CROSS_FLAGS) GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../build/apfslens-macos-arm64 .
	cd src/cross && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(CROSS_FLAGS) GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../build/apfslens-macos-x64 .

# Windows 原生辅助模块（C++ 搬运 / C 分区核对），需要 mingw-w64
native:
	x86_64-w64-mingw32-g++-posix -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o build/apfslens_native.exe native/native.cpp
	i686-w64-mingw32-g++-posix  -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o build/apfslens_native-x86.exe native/native.cpp
	x86_64-w64-mingw32-gcc-posix -O2 -static -o build/apfslens_probe.exe native/probe.c
	i686-w64-mingw32-gcc-posix  -O2 -static -o build/apfslens_probe-x86.exe native/probe.c

# 重新生成 Windows 图标资源（一般不需要，.syso 已入库）
icons:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico ../assets/icon.ico -o rsrc_windows_amd64.syso
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run github.com/akavel/rsrc@v0.10.2 -arch 386   -ico ../assets/icon.ico -o rsrc_windows_386.syso
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run github.com/akavel/rsrc@v0.10.2 -arch arm64 -ico ../assets/icon.ico -o rsrc_windows_arm64.syso

test:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(GO) test -count=1 .
	cd src/cross && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(GO) vet ./...

vet:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows $(GO) vet ./...
	cd src/cross && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=linux $(GO) vet ./...

package: linux darwin
	bash packaging/linux-dir.sh
	bash packaging/macos-app.sh $(VER)

clean:
	rm -rf build
