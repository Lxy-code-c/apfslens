//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"
)

var (
	logMu   sync.Mutex
	logFile *os.File
)

// initCrashLog 打开 %LOCALAPPDATA%\UDiskTool\log.txt，
// 并把 Go 运行时致命错误（并发写 map、nil 解引用等）也重定向到这个文件，
// 这样即使程序瞬间闪退，也留下一份可读的死亡现场。
func initCrashLog() {
	dir := appDataDir()
	f, err := os.OpenFile(dir+"\\log.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	logFile = f
	debug.SetTraceback("all")
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	logf("=== start %s %s ===", appVersion, time.Now().Format("2006-01-02 15:04:05"))
}

func logf(format string, a ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile == nil {
		return
	}
	fmt.Fprintf(logFile, time.Now().Format("15:04:05.000")+" "+format+"\n", a...)
}

// logCall 记录一次接口调用与耗时，用于定位「在哪一步闪退」。
func logCall(name string) func() {
	t0 := time.Now()
	logf(">> %s", name)
	return func() {
		logf("<< %s %s", name, time.Since(t0).Truncate(time.Millisecond))
	}
}

// goSafe 代替裸 go func(){}：任何 goroutine panic 都记日志而不是直接带走进程。
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf("PANIC in %s: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
