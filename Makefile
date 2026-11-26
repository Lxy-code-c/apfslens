GO ?= go
GOTOOLCHAIN = go1.27.1
LDFLAGS = -H windowsgui -s -w
VER ?= 2026Y11M26D-3

.PHONY: all windows test vet native clean

all: windows native

windows:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o ../U盘格式化工具.exe .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=386   $(GO) build -ldflags "$(LDFLAGS)" -o ../U盘格式化工具-x86.exe .
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o ../U盘格式化工具-arm64.exe .

test:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod $(GO) test -count=1 .

vet:
	cd src && GOTOOLCHAIN=$(GOTOOLCHAIN) GOFLAGS=-mod=mod GOOS=windows $(GO) vet ./...

native:
	x86_64-w64-mingw32-g++-posix -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o udisk_native.exe native/native.cpp
	i686-w64-mingw32-g++-posix  -O2 -std=c++11 -static -static-libgcc -static-libstdc++ -o udisk_native-x86.exe native/native.cpp
	x86_64-w64-mingw32-gcc-posix -O2 -static -o udisk_probe.exe native/probe.c
	i686-w64-mingw32-gcc-posix  -O2 -static -o udisk_probe-x86.exe native/probe.c

clean:
	rm -f *.exe
