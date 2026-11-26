package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed ui.html
var uiFS embed.FS

const appVersion = "1.0.0"

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// 端口起不来时给一句人话，不弹命令行。
		notifyFatal("启动失败：无法监听本地端口。\n" + err.Error())
		return
	}
	addr := ln.Addr().(*net.TCPAddr)
	url := fmt.Sprintf("http://127.0.0.1:%d/", addr.Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/volumes", handleVolumes)
	mux.HandleFunc("/api/list", handleList)
	mux.HandleFunc("/api/file", handleDownload)
	mux.HandleFunc("/api/search", handleSearch)
	mux.HandleFunc("/api/snaps", handleSnaps)
	mux.HandleFunc("/api/sys", handleSys)
	mux.Handle("/", http.FileServer(http.FS(mustSub(uiFS))))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		_ = srv.Serve(ln)
	}()
	openWindow(url)
	select {}
}

func mustSub(f embed.FS) fs.FS {
	sub, err := fs.Sub(f, ".")
	if err != nil {
		panic(err)
	}
	return sub
}

// 用系统默认浏览器当窗口（Linux 走 xdg-open，macOS 走 open），
// 进程本身不带终端：macOS 是 .app，Linux 是桌面快捷方式。
func openWindow(url string) {
	// 自动化自检用：不弹浏览器，只把地址打到标准输出（普通使用不会走到）。
	if os.Getenv("APFSLENS_NO_BROWSER") == "1" {
		fmt.Println(url)
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("gio"); err == nil {
			cmd = exec.Command("gio", "open", url)
		}
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}

func notifyFatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("zenity"); err == nil {
			_ = exec.Command("zenity", "--error", "--text", msg).Run()
		}
	}
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func downloadsDir() string {
	return filepath.Join(homeDir(), "Downloads")
}
