//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed ui.html
var uiHTML string

const appTitle = "U盘格式化工具 · APFS 读写助手 " + appVersion

type logSink struct{}

func (logSink) Write(p []byte) (int, error) {
	logf("stdlog: %s", string(p))
	return len(p), nil
}

func runApp() {
	// WebView2 的窗口与消息循环必须在同一个 OS 线程上：先钉住主线程。
	runtime.LockOSThread()
	initCrashLog()
	log.SetOutput(logSink{})
	defer func() {
		if r := recover(); r != nil {
			logf("FATAL in runApp: %v\n%s", r, debug.Stack())
		}
	}()

	dataDir := appDataDir() + "\\webview"
	_ = os.MkdirAll(dataDir, 0o755)

	if !webview2Runtime() {
		logf("WebView2 runtime not found, using browser mode")
		browserFallback("这台机器没有安装 WebView2 运行时")
		return
	}
	logf("checkpoint: create webview (data=%s)", dataDir)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  dataDir,
		WindowOptions: webview2.WindowOptions{
			Title:  appTitle,
			Width:  1360,
			Height: 860,
			Center: true,
		},
	})
	if w == nil {
		// 创建失败（多数是没装 WebView2 运行时）：不静默退出，
		// 改成用默认浏览器承载同一套界面，功能完全一致。
		browserFallback("NewWithOptions 返回 nil（缺少 WebView2 运行时 / DLL 加载失败）")
		return
	}
	logf("checkpoint: webview created")
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC in destroy: %v", r)
		}
		w.Destroy()
	}()

	bindAll(w)
	logf("checkpoint: bindings ready")

	goSafe("maximizeLater", func() { maximizeLater(appTitle) })
	goSafe("warmScan", func() {
		// 启动后先把磁盘列表预热一次（用户点进界面时直接命中缓存，
		// 不用再等 PowerShell 那几百毫秒）。
		time.Sleep(300 * time.Millisecond)
		_, _ = scanDisks()
		logf("scan warm-up done")
	})
	goSafe("autoMountAll", func() {
		time.Sleep(1500 * time.Millisecond)
		autoMountAll()
	})

	logf("checkpoint: set html (%d bytes)", len(uiHTML))
	w.SetHtml(uiHTML)
	logf("checkpoint: entering message loop")
	w.Run()
	logf("checkpoint: Run returned (window closed or Big5 exit)")
}

func maximizeLater(title string) {
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC in maximizeLater: %v", r)
		}
	}()
	for i := 0; i < 80; i++ {
		time.Sleep(100 * time.Millisecond)
		h := findWindowByTitle(title)
		if h != 0 {
			procShowWindow.Call(h, 3) // SW_MAXIMIZE
			procSetForeground.Call(h)
			logf("window maximized")
			return
		}
	}
	logf("window not found for maximize")
}

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW   = user32.NewProc("FindWindowW")
	procShowWindow    = user32.NewProc("ShowWindow")
	procSetForeground = user32.NewProc("SetForegroundWindow")
)

func findWindowByTitle(title string) uintptr {
	t, _ := windows.UTF16PtrFromString(title)
	h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	return h
}

func recoverTo(out *string) {
	if r := recover(); r != nil {
		logf("PANIC in rpc: %v\n%s", r, debug.Stack())
		*out = jerr(fmt.Sprintf("内部错误：%v（详情见 log.txt）", r))
	}
}

func s0(fn func() string) func() string {
	return func() (out string) { defer recoverTo(&out); return fn() }
}
func s1(fn func(string) string) func(string) string {
	return func(a string) (out string) { defer recoverTo(&out); return fn(a) }
}
func s2(fn func(string, string) string) func(string, string) string {
	return func(a, b string) (out string) { defer recoverTo(&out); return fn(a, b) }
}
func s3(fn func(string, string, string) string) func(string, string, string) string {
	return func(a, b, c string) (out string) { defer recoverTo(&out); return fn(a, b, c) }
}
func s4(fn func(string, string, string, string) string) func(string, string, string, string) string {
	return func(a, b, c, d string) (out string) { defer recoverTo(&out); return fn(a, b, c, d) }
}
func s5(fn func(string, string, string, string, string) string) func(string, string, string, string, string) string {
	return func(a, b, c, d, e string) (out string) { defer recoverTo(&out); return fn(a, b, c, d, e) }
}
func s6(fn func(string, string, string, string, string, string) string) func(string, string, string, string, string, string) string {
	return func(a, b, c, d, e, f string) (out string) { defer recoverTo(&out); return fn(a, b, c, d, e, f) }
}

func mustBind(w webview2.WebView, name string, f any) {
	if err := w.Bind(name, f); err != nil {
		logf("BIND FAIL %s: %v", name, err)
	}
}

func bindAll(w webview2.WebView) {
	mustBind(w, "apiScan", s0(func() string { return apiScan() }))
	mustBind(w, "apiDoctor", s0(func() string { return apiDoctor() }))
	mustBind(w, "apiSupport", s0(func() string { return apiSupport() }))
	mustBind(w, "apiApfsDevices", s0(func() string { return apiApfsDevices() }))
	mustBind(w, "apiApfsMounts", s0(func() string { return apiApfsMountList() }))
	mustBind(w, "apiMounts", s0(func() string { return apiMounts() }))
	mustBind(w, "apiElevate", s0(func() string { return apiElevate() }))

	mustBind(w, "apiTask", s1(func(a string) string { return apiTask(a) }))
	mustBind(w, "apiPartTable", s1(func(a string) string { return apiPartTable(a) }))
	mustBind(w, "apiDuMedia", s1(func(a string) string { return apiDuMedia(a) }))
	mustBind(w, "apiDuTree", s1(func(a string) string { return apiDuTree(a) }))
	mustBind(w, "apiApfsVolumes", s1(func(a string) string { return apiApfsVolumes(a) }))
	mustBind(w, "apiApfsTemplateInfo", s1(func(a string) string { return apiApfsTemplateInfo(a) }))
	mustBind(w, "apiApfsMountDrop", s1(func(a string) string { return apiApfsMountDrop(a) }))
	mustBind(w, "apiMount", s1(func(a string) string { return apiMount(a) }))
	mustBind(w, "apiUnmount", s1(func(a string) string { return apiUnmount(a) }))
	mustBind(w, "apiOpen", s1(func(a string) string { return apiOpen(a) }))

	mustBind(w, "apiApfsInfo", s3(func(a, b, c string) string { return apiApfsInfo(a, pUint(b), pUint(c)) }))
	mustBind(w, "apiApfsSnapshots", s3(func(a, b, c string) string { return apiApfsSnapshots(a, pUint(b), pUint(c)) }))
	mustBind(w, "apiDuVerify", s3(func(a, b, c string) string { return apiDuVerify(a, b, c) }))
	mustBind(w, "apiPartExec", s3(func(a, b, c string) string { return apiPartExec(a, b, c) }))

	mustBind(w, "apiApfsBigFiles", s4(func(a, b, c, d string) string { return apiApfsBigFiles(a, pUint(b), pUint(c), d) }))
	mustBind(w, "apiApfsMountSet", s4(func(a, b, c, d string) string { return apiApfsMountSet(a, b, c, d) }))
	mustBind(w, "apiDuSpace", s4(func(a, b, c, d string) string { return apiDuSpace(a, pUint(b), pUint(c), d) }))
	mustBind(w, "apiFormat", s4(func(a, b, c, d string) string { return apiFormat(a, b, c, d) }))
	mustBind(w, "apiApfsApplyTemplate", s4(func(a, b, c, d string) string { return apiApfsApplyTemplate(a, b, c, d) }))
	mustBind(w, "apiApfsUnlock", s4(func(a, b, c, d string) string { return apiApfsUnlock(a, pUint(b), pUint(c), d) }))
	mustBind(w, "apiPartTag", s4(func(a, b, c, d string) string { return apiPartTag(a, b, c, d) }))

	mustBind(w, "apiApfsList", s5(func(a, b, c, d, e string) string { return apiApfsList(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsExport", s5(func(a, b, c, d, e string) string { return apiApfsExport(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsDownload", s5(func(a, b, c, d, e string) string { return apiApfsDownload(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsFileInfo", s5(func(a, b, c, d, e string) string { return apiApfsFileInfo(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsSearch", s5(func(a, b, c, d, e string) string { return apiApfsSearch(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsPreview", s5(func(a, b, c, d, e string) string { return apiApfsPreview(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsMount", s5(func(a, b, c, d, e string) string { return apiApfsMount(a, pUint(b), pUint(c), d, e) }))
	mustBind(w, "apiApfsQuick", s5(func(a, b, c, d, e string) string { return apiApfsQuick(a, b, c, d, e) }))

	mustBind(w, "apiPartFormatOne", s6(func(a, b, c, d, e, f string) string { return apiPartFormatOne(a, b, c, d, e, f) }))
	mustBind(w, "apiApfsDiff", s6(func(a, b, c, d, e, f string) string { return apiApfsDiff(a, pUint(b), pUint(c), d, e, f) }))
}
