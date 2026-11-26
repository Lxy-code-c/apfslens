//go:build !windows

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func netListen(network, addr string) (net.Listener, error) { return net.Listen(network, addr) }
func osExit(code int)                               { os.Exit(code) }

// 非 Windows 平台（Linux / macOS）的图形界面入口。
// 本程序只提供图形版，不暴露任何命令行子命令；启动后直接打开本机页面窗口。
// 说明：磁盘扫描、盘符挂载、格式化等能力目前只在 Windows 上接通；
// Linux / macOS 版本为预览壳，APFS 只读解析引擎已随包编译，后续版本接入。

func runApp() {
	addr := startGUIServer()
	openBrowser(addr)
	// 常驻直到页面请求 /bye（关闭窗口时退出）。
	select {}
}

func startGUIServer() string {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, statusPage())
	})
	mux.HandleFunc("/bye", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() { time.Sleep(200 * time.Millisecond); osExit(0) }()
	})
	ln, err := netListen("tcp", "127.0.0.1:0")
	if err != nil {
		// 没有图形环境时也不往终端堆命令行输出，直接退出。
		osExit(1)
		return ""
	}
	go func() { _ = http.Serve(ln, mux) }()
	return "http://" + ln.Addr().String() + "/"
}

func openBrowser(addr string) {
	if addr == "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", addr)
	case "linux":
		cmd = exec.Command("xdg-open", addr)
	default:
		return
	}
	_ = cmd.Start()
}

func statusPage() string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ApfsLens</title>
<style>
  :root{color-scheme:dark}
  *{box-sizing:border-box;margin:0}
  body{font-family:-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;
    background:linear-gradient(135deg,#0b2a4a,#12708a);min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
  .card{background:rgba(255,255,255,.08);border:1px solid rgba(255,255,255,.15);border-radius:20px;
    padding:36px 34px;max-width:560px;color:#eaf4ff;line-height:1.7}
  h1{font-size:22px;margin-bottom:12px}
  p{color:#cfe3f5;font-size:15px}
  .tag{display:inline-block;margin-top:16px;padding:6px 12px;border-radius:999px;background:rgba(255,255,255,.12);font-size:13px}
</style></head><body>
<div class="card">
  <h1>ApfsLens</h1>
  <p>当前平台：` + runtime.GOOS + ` / ` + runtime.GOARCH + `。</p>
  <p>磁盘扫描、盘符挂载、格式化等能力目前在 Windows 版本上完整可用；
  本平台为预览版本，APFS 只读解析引擎已随包编译，镜像浏览、文件导出将在后续版本接通。</p>
  <p>本程序只提供图形界面，不包含命令行操作。关闭本页面即退出程序。</p>
  <span class="tag">Linux / macOS 预览版</span>
</div></body></html>`
}
