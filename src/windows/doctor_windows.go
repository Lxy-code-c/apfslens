//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// apiDoctor：多引擎诊断。Go 负责收集事实，Python 负责排版与给出建议；
// 没有 Python 时用 Go 自己的排版（保证任何机器上都能出报告）。
func apiDoctor() string {
	facts := collectDoctorFacts()
	if txt, err := runPythonDoctor(facts); err == nil && strings.TrimSpace(txt) != "" {
		return jok(map[string]any{"text": txt, "engine": "python"})
	}
	return jok(map[string]any{"text": formatDoctorFallback(facts), "engine": "go"})
}

type doctorFacts struct {
	Version  string
	Arch     string
	Admin    bool
	WebView2 bool
	LogTail  []string
	Diag     string
	Disks    []string
	Native   []string
	Time     string
	LogPath  string
	DiagPath string
	UserData string
}

func collectDoctorFacts() doctorFacts {
	f := doctorFacts{
		Version:  appVersion,
		Arch:     runtime.GOARCH,
		Admin:    isAdmin(),
		WebView2: webview2Runtime(),
		Time:     time.Now().Format("2006-01-02 15:04:05"),
		UserData: appDataDir(),
	}
	f.LogPath = filepath.Join(appDataDir(), "log.txt")
	if b, err := os.ReadFile(f.LogPath); err == nil {
		lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		if len(lines) > 120 {
			lines = lines[len(lines)-120:]
		}
		f.LogTail = lines
	}
	f.DiagPath = filepath.Join(downloadsDir(), "apfslens-diag.txt")
	if b, err := os.ReadFile(f.DiagPath); err == nil {
		f.Diag = string(b)
		if len(f.Diag) > 4000 {
			f.Diag = f.Diag[:4000]
		}
	}
	// 盘：只报数量/类型，不把序列号之类带出去
	disks, _ := scanDisks()
	for _, d := range disks {
		mode := "只读"
		if _, err := openWinDevice(d.Path, 0, d.Size); err == nil {
			mode = "可打开"
		}
		f.Disks = append(f.Disks, fmt.Sprintf("%s  %s  %s  分区=%d  APFS=%v  系统盘=%v  %s",
			d.Path, d.Model, humanBytes(d.Size), len(d.Parts), d.Apfs, d.System, mode))
	}
	for _, n := range []string{"apfslens_native.exe", "apfslens_probe.exe", "apfslens_doctor.py"} {
		p := filepath.Join(filepath.Dir(mustExecutable()), n)
		if _, err := os.Stat(p); err == nil {
			f.Native = append(f.Native, n+" 存在")
		} else {
			f.Native = append(f.Native, n+" 缺失")
		}
	}
	return f
}

func mustExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return p
}

func factsToText(f doctorFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version=%s\narch=%s\nadmin=%v\nwebview2=%v\ntime=%s\n",
		f.Version, f.Arch, f.Admin, f.WebView2, f.Time)
	fmt.Fprintf(&b, "log_path=%s\ndiag_path=%s\nuserdata=%s\n", f.LogPath, f.DiagPath, f.UserData)
	for _, d := range f.Disks {
		b.WriteString("disk=" + d + "\n")
	}
	for _, n := range f.Native {
		b.WriteString("native=" + n + "\n")
	}
	b.WriteString("diag_begin\n" + f.Diag + "\ndiag_end\n")
	b.WriteString("log_begin\n")
	for _, l := range f.LogTail {
		b.WriteString(l + "\n")
	}
	b.WriteString("log_end\n")
	return b.String()
}

func runPythonDoctor(f doctorFacts) (string, error) {
	script := filepath.Join(filepath.Dir(mustExecutable()), "apfslens_doctor.py")
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("没有诊断脚本")
	}
	py := ""
	for _, cand := range []string{"py", "python", "python3"} {
		if p, err := exec.LookPath(cand); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		return "", fmt.Errorf("本机没有 Python")
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("apfslens_doc_%d.txt", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, []byte(factsToText(f)), 0o600); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	args := []string{script, tmp}
	if filepath.Base(py) == "py" {
		args = append([]string{"-3"}, args...)
	}
	out, err := runHidden(py, args...)
	if err != nil {
		return "", fmt.Errorf("python 诊断失败：%v %s", err, out)
	}
	return out, nil
}

func formatDoctorFallback(f doctorFacts) string {
	var b strings.Builder
	b.WriteString("=== ApfsLens 诊断报告 ===\n")
	fmt.Fprintf(&b, "版本 %s（%s）  时间 %s\n", f.Version, f.Arch, f.Time)
	line := func(k, v string) { fmt.Fprintf(&b, "%-12s %s\n", k, v) }
	line("管理员权限", boolTxt(f.Admin, "有", "无（读写原始设备会受限，建议右键以管理员运行）"))
	line("WebView2", boolTxt(f.WebView2, "已安装", "缺失（会用浏览器兜底界面）"))
	line("用户目录", f.UserData)
	b.WriteString("\n--- 磁盘 ---\n")
	if len(f.Disks) == 0 {
		b.WriteString("没有扫描到磁盘（检查权限或刷新）\n")
	}
	for _, d := range f.Disks {
		b.WriteString("  " + d + "\n")
	}
	b.WriteString("\n--- 模块 ---\n")
	for _, n := range f.Native {
		b.WriteString("  " + n + "\n")
	}
	b.WriteString("\n--- APFS 诊断 ---\n")
	if f.Diag == "" {
		b.WriteString("没有 apfslens-diag.txt（还没遇到过扫描失败，或路径不同）\n")
	} else {
		b.WriteString(f.Diag + "\n")
	}
	b.WriteString("\n--- 日志尾部 ---\n")
	if len(f.LogTail) == 0 {
		b.WriteString("没有日志\n")
	} else {
		for _, l := range f.LogTail {
			b.WriteString("  " + l + "\n")
		}
	}
	return b.String()
}

func boolTxt(v bool, a, b string) string {
	if v {
		return a
	}
	return b
}

// 让 encoding/json 参与编译，避免未使用（Python 通道未来可能返回结构化结果）。
var _ = json.Marshal
