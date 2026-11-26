//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// nativeCopyExtents 调 C++ 模块 udisk_native.exe：按给定物理段并行拷贝。
// 它只搬字节，不解析 APFS，所以不会和 Go 端的解析结果产生分歧；
// 任何失败都返回 error，由 copyFileTo 回退到 Go 路径。
func nativeCopyExtents(dev string, partOff int64, exts []byteExtent, dst string, total int64) error {
	exe := nativeExePath()
	if exe == "" {
		return fmt.Errorf("no native module")
	}
	job := filepath.Join(os.TempDir(), fmt.Sprintf("udisk_job_%d.txt", time.Now().UnixNano()))
	var b strings.Builder
	b.WriteString("src=" + dev + "\n")
	b.WriteString("dst=" + dst + "\n")
	fmt.Fprintf(&b, "total=%d\n", total)
	for _, e := range exts {
		fmt.Fprintf(&b, "ext %d %d %d\n", partOff+e.Off, e.Len, e.Dst)
	}
	if err := os.WriteFile(job, []byte(b.String()), 0o600); err != nil {
		return err
	}
	defer os.Remove(job)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "copy", job)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("native copy 退出：%v %s", err, strings.TrimSpace(string(out)))
	}
	var res struct {
		OK    bool   `json:"ok"`
		Bytes int64  `json:"bytes"`
		Error string `json:"error"`
	}
	if e := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &res); e != nil {
		return fmt.Errorf("native copy 输出无法解析：%v", e)
	}
	if !res.OK {
		return fmt.Errorf("native copy：%s", res.Error)
	}
	if res.Bytes != total {
		return fmt.Errorf("native copy 字节数不符：%d != %d", res.Bytes, total)
	}
	return nil
}

// nativeExePath 找与主程序同目录的 C++ 模块；32 位系统用 -x86 版。
func nativeExePath() string {
	dir, err := os.Executable()
	if err != nil {
		return ""
	}
	dir = filepath.Dir(dir)
	name := "udisk_native.exe"
	if runtime.GOARCH == "386" {
		name = "udisk_native-x86.exe"
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func init() { nativeCopyHook = nativeCopyExtents }
