package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// 系统调用辅助：管理员判断、隐藏窗口执行 PowerShell / diskpart
// ---------------------------------------------------------------------------

const createNoWindow = 0x08000000

func isAdmin() bool {
	var token windows.Token
	proc, _ := windows.GetCurrentProcess()
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	var elevation struct {
		TokenIsElevated uint32
	}
	var outLen uint32
	err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &outLen)
	if err != nil {
		return false
	}
	return elevation.TokenIsElevated != 0
}

func hiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

func runHidden(name string, args ...string) (string, error) {
	cmd := hiddenCmd(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func powershellPath() string {
	p := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "powershell.exe"
}

func runPS(script string) (string, error) {
	// -NoProfile 避免加载用户配置导致卡顿；输出统一 UTF-8
	full := "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;$ErrorActionPreference='Stop';" + script
	cmd := hiddenCmd(powershellPath(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", full)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	return out, err
}

func runPSJson(script string, v any) error {
	out, err := runPS(script)
	if err != nil && out == "" {
		return fmt.Errorf("PowerShell 执行失败：%v", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return fmt.Errorf("PowerShell 没有输出")
	}
	// 去掉可能的 BOM / 杂音
	if i := strings.Index(out, "["); i > 0 {
		out = out[i:]
	}
	if i := strings.Index(out, "{"); i > 0 && !strings.HasPrefix(out, "[") {
		out = out[i:]
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("解析 PowerShell 输出失败：%v（原始输出前 200 字：%s）", err, truncate(out, 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// 磁盘枚举
// ---------------------------------------------------------------------------

type partInfo struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Fs     string `json:"fs"`
	Size   int64  `json:"size"`
	Letter string `json:"letter"`
	Label  string `json:"label"`
	Offset int64  `json:"offset"`
	GptID  string `json:"gpt,omitempty"`
}

type diskInfo struct {
	Name    string     `json:"name"`
	Path    string     `json:"path"`
	Number  int        `json:"number"`
	Size    int64      `json:"size"`
	Model   string     `json:"model"`
	Tran    string     `json:"tran"`
	System  bool       `json:"system"`
	Table   string     `json:"table"`
	Parts   []partInfo `json:"parts"`
	Apfs    bool       `json:"apfs"`
	Mounted bool       `json:"mounted"`
	Serial  string     `json:"serial,omitempty"`
	Health  string     `json:"health,omitempty"`
	Media   string     `json:"media,omitempty"`
}

type psPart struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Fs     string `json:"fs"`
	Size   int64  `json:"size"`
	Letter string `json:"letter"`
	Label  string `json:"label"`
	Offset int64  `json:"offset"`
	Gpt    string `json:"gpt"`
}

type psDisk struct {
	Number int      `json:"number"`
	Model  string   `json:"model"`
	Size   int64    `json:"size"`
	Bus    string   `json:"bus"`
	Table  string   `json:"table"`
	System bool     `json:"system"`
	Serial string   `json:"serial"`
	Health string   `json:"health"`
	Media  string   `json:"media"`
	Parts  []psPart `json:"parts"`
}

const diskQueryScript = `
$sysDrive = ($env:SystemDrive -replace ':','')
$out = @()
foreach ($d in (Get-Disk | Sort-Object Number)) {
  $parts = @()
  foreach ($p in (Get-Partition -DiskNumber $d.Number -ErrorAction SilentlyContinue | Sort-Object PartitionNumber)) {
    $v = Get-Volume -Partition $p -ErrorAction SilentlyContinue | Select-Object -First 1
    $parts += [pscustomobject]@{
      index  = [int]$p.PartitionNumber
      type   = [string]$p.Type
      fs     = [string]($(if ($v) { $v.FileSystem } else { '' }))
      size   = [int64]$p.Size
      letter = [string]($(if ($v -and $v.DriveLetter) { [string]$v.DriveLetter } else { '' }))
      label  = [string]($(if ($v) { $v.FileSystemLabel } else { '' }))
      offset = [int64]$p.Offset
      gpt    = [string]$p.GptType
    }
  }
  $out += [pscustomobject]@{
    number = [int]$d.Number
    model  = [string]$d.FriendlyName
    size   = [int64]$d.Size
    bus    = [string]$d.BusType
    table  = [string]$d.PartitionStyle
    system = [bool]($d.IsSystem -or $d.IsBoot -or (($parts | Where-Object { $_.letter -eq $sysDrive }).Count -gt 0))
    serial = [string]$d.SerialNumber
    health = [string]$d.HealthStatus
    media  = [string]$d.MediaType
  }
}
ConvertTo-Json -InputObject $out -Depth 6 -Compress
`

var (
	scanMu     sync.Mutex
	scanCond   = sync.NewCond(&scanMu)
	scanAt     time.Time
	scanCached []diskInfo
	scanning   bool
)

// invalidateScanCache 让下一次 scanDisks 重新读磁盘布局（改过分区表之后必须调用）。
func invalidateScanCache() {
	scanMu.Lock()
	scanCached = nil
	scanAt = time.Time{}
	scanMu.Unlock()
}

// scanTTL 缓存时长。原来只有 2 秒，界面每翻一页都要重新拉起一次
// PowerShell + WMI（几百毫秒），手感就是「点哪都卡一下」。
const scanTTL = 6 * time.Second

func scanDisks() ([]diskInfo, error) {
	scanMu.Lock()
	for {
		if scanCached != nil && time.Since(scanAt) < scanTTL {
			out := scanCached
			scanMu.Unlock()
			return out, nil
		}
		if !scanning {
			break
		}
		// 已经有人在扫：等它，别并发再拉一个 PowerShell
		scanCond.Wait()
	}
	scanning = true
	scanMu.Unlock()

	list, err := scanDisksRaw()

	scanMu.Lock()
	if err == nil {
		scanCached, scanAt = list, time.Now()
	}
	scanning = false
	scanCond.Broadcast()
	scanMu.Unlock()
	return list, err
}

func scanDisksRaw() ([]diskInfo, error) {
	var list []psDisk
	script := "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;" + diskQueryScript
	out, err := runPS(script)
	if err != nil && out == "" {
		return nil, fmt.Errorf("读取磁盘列表失败：%v", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("没有读到磁盘信息")
	}
	// PowerShell 只有一块磁盘时 ConvertTo-Json 输出的是对象而不是数组，
	// 单盘虚拟机（最常见的情况）会因此解析失败 —— 这里统一补成数组。
	first := byte(0)
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == 0xef || c == 0xbb || c == 0xbf {
			continue
		}
		first = c
		break
	}
	if first == '{' {
		out = "[" + out + "]"
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("解析磁盘列表失败：%v（原始输出前 200 字：%s）", err, truncate(out, 200))
	}
	var res []diskInfo
	for _, d := range list {
		di := diskInfo{
			Name:   fmt.Sprintf("磁盘 %d", d.Number),
			Path:   fmt.Sprintf("\\\\.\\PhysicalDrive%d", d.Number),
			Number: d.Number,
			Size:   d.Size,
			Model:  d.Model,
			Tran:   strings.ToLower(d.Bus),
			System: d.System,
			Table:  d.Table,
			Serial: d.Serial,
			Health: d.Health,
			Media:  d.Media,
		}
		for _, p := range d.Parts {
			di.Parts = append(di.Parts, partInfo{
				Index: p.Index, Type: p.Type, Fs: strings.ToLower(p.Fs), Size: p.Size,
				Letter: strings.ToUpper(p.Letter), Label: p.Label, Offset: p.Offset, GptID: p.Gpt,
			})
			if p.Letter != "" {
				di.Mounted = true
			}
		}
		if len(di.Parts) == 0 && d.Size > 0 {
			// Get-Partition 有时会因为权限/未知分区表直接返回空，
			// 这时退到 WMI 的 Win32_DiskPartition 拿原始分区布局。
			if wm := wmiParts(d.Number); len(wm) > 0 {
				di.Parts = wm
			}
		}
		di.Apfs = detectAPFSParts(di)
		res = append(res, di)
	}
	return res, nil
}

const gptAPFSGUID = "7C3457EF-0000-11AA-AA11-00306543ECAC"

// isAPFSGptType 只看分区表类型 GUID。
// 这比读扇区找 NXSB 更可靠：不依赖读盘权限、不受 4Kn 扇区对齐影响，
// 加密卷（FileVault）也一样能在 GPT 里看到这个类型。
func isAPFSGptType(gpt string) bool {
	g := strings.ToUpper(strings.Trim(gpt, "{}[] "))
	g = strings.ReplaceAll(g, " ", "")
	return strings.HasPrefix(g, gptAPFSGUID) || strings.Contains(g, "7C3457EF")
}

// 在分区起始处探测 NXSB 魔术，判断是否 APFS
func detectAPFSParts(d diskInfo) bool {
	// 1) 自己解析原始分区表：GPT 类型 GUID、APM 分区名（Apple_APFS）
	if parts, _, err := parsePartitionTable(d.Path); err == nil {
		for _, p := range parts {
			if isAPFSGptType(p.GUID) || strings.Contains(strings.ToUpper(p.Label), "APFS") {
				return true
			}
		}
	}
	// 2) 再信 Windows 报的分区表类型：APFS 的 GUID 是唯一标识，加密与否都一样
	for _, p := range d.Parts {
		if isAPFSGptType(p.GptID) {
			return true
		}
	}
	// 3) 最后按偏移读块 0 找 NXSB。候选偏移必须同时包含「自解析」和
	//    「Windows 分区表」两种来源 —— Windows 列不出分区的盘只能靠自解析。
	//    注意 readRaw 内部自己加锁，外面再加锁就是自死锁（旧版就死在这里）。
	// 系统盘永远不允许格式化，也没必要为它开设备读块（这一步在慢盘上很明显），
	// 直接跳过探测能省下不少每次刷新的时间。
	if d.System {
		// 只跳真正的系统盘；不要用 Number==0 判断，
		// 单盘虚拟机里唯一那块盘就是 0 号，跳了会漏掉 APFS。
		return false
	}
	offs := apfsProbeOffsets(d)
	if len(offs) > 8 {
		offs = offs[:8]
	}
	dev, err := openWinDevice(d.Path, 0, d.Size)
	if err != nil {
		return false
	}
	defer dev.Close()
	buf := make([]byte, 4096)
	for _, off := range offs {
		n, rerr := dev.readRaw(buf, off)
		if rerr != nil || n < 36 {
			continue
		}
		if string(buf[32:36]) == "NXSB" {
			return true
		}
	}
	// 4) 最后交给独立的 C 引擎再判一次：它自己解析 GPT/MBR 并校验 CRC，
	//    与上面的 Go 解析不是同一套代码。任一方看到 APFS 就算有——
	//    这类「漏判成没有 APFS」的盘，用户看到的就是「识别不到」。
	if parts, perr := cProbeAPFS(d.Path, 0); perr == nil {
		for _, p := range parts {
			if p.NXSB || p.Size > 0 {
				return true
			}
		}
	} else {
		logf("probe 交叉核对失败（不影响结论）：%v", perr)
	}
	return false
}

// apfsProbeOffsets 给出需要探测 NXSB 的候选偏移：自解析分区 → Windows 分区 → 整盘起点。
func apfsProbeOffsets(d diskInfo) []int64 {
	var offs []int64
	spans, _ := apfsSpans(d)
	for _, s := range spans {
		offs = append(offs, s.Off)
	}
	hasZero := false
	for _, o := range offs {
		if o == 0 {
			hasZero = true
		}
	}
	if !hasZero {
		offs = append(offs, 0)
	}
	return offs
}

func wmiParts(diskNumber int) []partInfo {
	script := fmt.Sprintf(`$p=Get-CimInstance -ClassName Win32_DiskPartition | Where-Object { $_.DiskIndex -eq %d }; $o=@($p | ForEach-Object { [pscustomobject]@{ index=[int]$_.Index; type=[string]$_.Type; fs=''; size=[int64]$_.Size; letter=''; label=''; offset=[int64]$_.StartingOffset; gpt='' } }); ConvertTo-Json -InputObject $o -Depth 4 -Compress`, diskNumber)
	var list []psPart
	if err := runPSJson(script, &list); err != nil {
		return nil
	}
	var out []partInfo
	for _, p := range list {
		if p.Size <= 0 {
			continue
		}
		out = append(out, partInfo{Index: p.Index, Type: p.Type, Size: p.Size, Offset: p.Offset})
	}
	return out
}

func readDiskBlock(diskPath string, offset int64, size int64, blockOff int64) ([]byte, error) {
	dev, err := openWinDevice(diskPath, offset, size)
	if err != nil {
		return nil, err
	}
	defer dev.Close()
	buf := make([]byte, 4096)
	if _, err := dev.ReadAt(buf, blockOff); err != nil {
		return nil, err
	}
	return buf, nil
}

var _ = strconv.Itoa
var _ = time.Now
