//go:build windows

package main

import "syscall"

// 高分屏上界面发糊的根因：进程没声明 DPI 感知，Windows 把整个窗口按
// 缩放比例拉伸成位图再交给屏幕，字和线全被重采样一次。
// 这里在任何窗口创建之前声明「每显示器 DPI 感知 v2」，让 WebView2 直接
// 按设备像素渲染，字就是清楚的。
func setDPIAware() {
	user32 := syscall.NewLazyDLL("user32.dll")
	shcore := syscall.NewLazyDLL("shcore.dll")

	if p := user32.NewProc("SetProcessDpiAwarenessContext"); p.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 是句柄 -4，
		// 32/64 位分别对应 0xFFFFFFFC / 0xFFFFFFFFFFFFFFFC。
		// ^uintptr(0)-3：32 位得 0xFFFFFFFC，64 位得 0xFFFFFFFFFFFFFFFC。
		ctx := ^uintptr(0) - 3
		if r, _, _ := p.Call(ctx); r != 0 {
			logf("dpi: per-monitor v2 ok")
			return
		}
	}
	if p := shcore.NewProc("SetProcessDpiAwareness"); p.Find() == nil {
		// PROCESS_PER_MONITOR_DPI_AWARE == 2
		if r, _, _ := p.Call(uintptr(2)); r == 0 {
			logf("dpi: per-monitor ok (shcore)")
			return
		}
	}
	if p := user32.NewProc("SetProcessDPIAware"); p.Find() == nil {
		if r, _, _ := p.Call(); r != 0 {
			logf("dpi: system aware ok")
			return
		}
	}
	logf("dpi: no awareness API available, UI may look scaled")
}
