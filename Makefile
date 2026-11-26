GO ?= go
GOTOOLCHAIN = go1.27.1
VER ?= 1.0.2
GOFLAGS = -mod=mod

WIN_DIR = src/windows
X_DIR   = src/cross
BUILD   = build

WIN_LDFLAGS = -H windowsgui -s -w
UI_LDFLAGS  = -s -w

.PHONY: all windows linux darwin native test vet package icons clean

all: windows linux darwin

# ---------------------------------------------------------------------------
# Windows：WebView2 原生窗口，三架构，无终端；图标由 src/windows/rsrc_windows_*.syso 嵌入
# ---------------------------------------------------------------------------
windows:
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../../$(BUILD)/apfslens-x64.exe .
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) GOOS=windows GOARCH=386   $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../../$(BUILD)/apfslens-x86.exe .
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(WIN_LDFLAGS)" -o ../../$(BUILD)/apfslens-arm64.exe .

# ---------------------------------------------------------------------------
# Linux：图形版，纯 Go 无 cgo；系统浏览器当窗口，只监听 127.0.0.1
# ---------------------------------------------------------------------------
linux:
	cd $(X_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../$(BUILD)/apfslens-linux-x64 .
	cd $(X_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../$(BUILD)/apfslens-linux-arm64 .

# ---------------------------------------------------------------------------
# macOS：同上；.app 由 packaging/macos-app.sh 打包，图标已入库
# ---------------------------------------------------------------------------
darwin:
	cd $(X_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../$(BUILD)/apfslens-macos-arm64 .
	cd $(X_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags "$(UI_LDFLAGS)" -o ../../$(BUILD)/apfslens-macos-x64 .

# Windows 原生辅助引擎（C++ 并行搬运 / C 分区交叉核对），需要 mingw-w64
native:
	x86_64-w64-mingw32-g++-posix -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o $(BUILD)/apfslens_native.exe native/native.cpp
	i686-w64-mingw32-g++-posix  -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o $(BUILD)/apfslens_native-x86.exe native/native.cpp
	x86_64-w64-mingw32-gcc-posix -O2 -static -o $(BUILD)/apfslens_probe.exe native/probe.c
	i686-w64-mingw32-gcc-posix  -O2 -static -o $(BUILD)/apfslens_probe-x86.exe native/probe.c

# 重新生成 Windows 图标资源（一般不需要，.syso 已入库）
icons:
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico ../../assets/icons/icon.ico -o rsrc_windows_amd64.syso
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run github.com/akavel/rsrc@v0.10.2 -arch 386   -ico ../../assets/icons/icon.ico -o rsrc_windows_386.syso
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run github.com/akavel/rsrc@v0.10.2 -arch arm64 -ico ../../assets/icons/icon.ico -o rsrc_windows_arm64.syso

test:
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) $(GO) test -count=1 .
	cd $(X_DIR)   && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) $(GO) vet ./...

vet:
	cd $(WIN_DIR) && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) GOOS=windows $(GO) vet ./...
	cd $(X_DIR)   && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=$(GOFLAGS) GOOS=linux   $(GO) vet ./...

package: linux darwin
	bash packaging/linux-dir.sh
	bash packaging/macos-app.sh $(VER)

clean:
	rm -rf $(BUILD)
