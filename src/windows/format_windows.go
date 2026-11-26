//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blacktop/go-apfs/types"
	"golang.org/x/sys/windows"
)

const (
	gptAPFS          = "{7C3457EF-0000-11AA-AA11-00306543ECAC}"
	gptHFS           = "{48465300-0000-11AA-AA11-00306543ECAC}"
	gptAppleRecovery = "{426F6F74-0000-11AA-AA11-00306543ECAC}"
	gptEFI           = "{C12A7328-F81F-11D2-BA4B-00A0C93EC93B}"
	gptMSData        = "{EBD0A0A2-B9E5-4433-87C0-68B6B72699C7}"
)

var diskOpMu sync.Mutex

// guardDisk 拒绝在任何系统盘上做破坏性操作（clean / 重建分区表 / 写镜像）。
func guardDisk(dev string) error {
	disks, err := scanDisks()
	if err != nil {
		// 拿不到磁盘信息就不许做破坏性操作：以前这里 return nil，
		// 等于「探测失败就放行」，有可能把系统盘擦掉。
		return fmt.Errorf("无法确认目标磁盘是不是系统盘（%v），为安全起见已拒绝操作", err)
	}
	found := false
	for _, d := range disks {
		if !strings.EqualFold(d.Path, dev) {
			continue
		}
		found = true
		if d.System || d.Number == 0 {
			return fmt.Errorf("这是系统盘（%s），为避免误擦系统，工具拒绝在它上面格式化或重建分区表", d.Model)
		}
	}
	if !found {
		// 设备列表里没有这台盘：可能是路径写错或刚拔出。
		// 这种情况绝不能当「安全」放行。
		return fmt.Errorf("设备 %s 不在当前磁盘列表中（可能已拔出），已拒绝破坏性操作", dev)
	}
	return nil
}

func diskNumberOf(dev string) int {
	i := strings.LastIndex(dev, "PhysicalDrive")
	if i < 0 {
		return -1
	}
	n := 0
	fmt.Sscanf(dev[i+len("PhysicalDrive"):], "%d", &n)
	return n
}

func runDiskpart(script string) (string, error) {
	f, err := os.CreateTemp("", "apfslens-*.txt")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	out, err := runHidden(filepath.Join(os.Getenv("SystemRoot"), "System32", "diskpart.exe"), "/s", path)
	// 分区表变了，磁盘布局缓存必须失效（所有调用方都走这里，一次改动全覆盖）
	invalidateScanCache()
	dropAllVolumeCachesAsync()
	return out, err
}

func fsDiskpartName(id string) (string, bool) {
	switch strings.ToLower(id) {
	case "ntfs":
		return "ntfs", true
	case "exfat":
		return "exfat", true
	case "fat32":
		return "fat32", true
	case "fat16":
		return "fat", true
	case "refs":
		return "refs", true
	}
	return "", false
}

// 支持列表
func fsSupport() []map[string]any {
	return []map[string]any{
		{"id": "ntfs", "name": "NTFS", "note": "Windows 原生", "available": true},
		{"id": "exfat", "name": "exFAT", "note": "U 盘首选", "available": true},
		{"id": "fat32", "name": "FAT32", "note": "单文件 4GB 上限", "available": true},
		{"id": "fat16", "name": "FAT16", "note": "老设备兼容", "available": true},
		{"id": "hfsplus", "name": "HFS+", "note": "标记类型，交给 Mac 抹掉", "available": true},
		{"id": "apfs", "name": "APFS", "note": "标记类型 + 模板还原", "available": true},
	}
}

// apiFormat：整盘重建分区表并格式化（异步任务）
func startFormat(dev, fs, mode, label string) (*taskRec, error) {
	num := diskNumberOf(dev)
	if num < 0 {
		return nil, fmt.Errorf("无法识别磁盘编号：%s", dev)
	}
	if !isAdmin() {
		return nil, fmt.Errorf("需要管理员权限")
	}
	if err := guardDisk(dev); err != nil {
		return nil, err
	}
	t := newTask("format", dev, fs, label, mode)
	goSafe("format", func() {
		diskOpMu.Lock()
		defer diskOpMu.Unlock()
		t.log("info", fmt.Sprintf("diskpart: select disk %d", num))
		t.stage("清理旧分区表", 0.1)

		lower := strings.ToLower(fs)
		f, native := fsDiskpartName(lower)
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("select disk %d\r\nclean\r\nconvert gpt\r\n", num))
		if native {
			t.stage("创建分区", 0.3)
			sb.WriteString("create partition primary\r\n")
			sb.WriteString(fmt.Sprintf("format fs=%s %s", f, map[bool]string{true: "quick", false: ""}[mode == "quick"]))
			if label != "" {
				sb.WriteString(fmt.Sprintf(" label=\"%s\"", sanitizeLabel(label)))
			}
			sb.WriteString("\r\nassign\r\n")
		} else {
			// APFS / HFS+：Windows 无法直接格式化，只建分区并标记类型
			t.stage("创建 Apple 分区", 0.3)
			sb.WriteString("create partition primary\r\n")
			if lower == "apfs" {
				sb.WriteString(fmt.Sprintf("set id=%s\r\n", gptAPFS))
			} else {
				sb.WriteString(fmt.Sprintf("set id=%s\r\n", gptHFS))
			}
		}
		t.stage("执行 diskpart", 0.5)
		out, err := runDiskpart(sb.String())
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(strings.Trim(line, "\r"))
			if line != "" {
				t.log("info", line)
			}
		}
		if err != nil {
			t.finish(false, fmt.Sprintf("diskpart 失败：%v", err))
			return
		}
		if !native {
			t.addProgress(0.3)
			t.log("ok", "分区已创建并标记为 "+strings.ToUpper(lower)+" 类型")
			if lower == "apfs" {
				t.log("info", "Windows 不能直接写出合法 APFS 容器；到 Mac 上用「磁盘工具 → 抹掉」即可成为真正的 APFS 卷")
				t.log("info", "也可以在本工具「分区管理 → APFS 模板还原」里写入一份 Mac 生成的空白 APFS 镜像")
			} else {
				t.log("info", "到 Mac 上用磁盘工具抹掉为 HFS+ 即可使用")
			}
		} else {
			t.addProgress(0.4)
			t.log("ok", "格式化完成："+strings.ToUpper(fs)+" · "+label)
		}
		dropAllVolumeCaches()
		time.Sleep(300 * time.Millisecond)
		t.finish(true, "磁盘操作完成")
	})
	return t, nil
}

func sanitizeLabel(s string) string {
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func dropAllVolumeCachesAsync() {
	// 放到 goroutine 里做：runDiskpart 可能本身就在持有 diskOpMu 的任务里，
	// 这里只要保证缓存失效，不需要阻塞调用方。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf("PANIC in dropAllVolumeCachesAsync: %v", r)
			}
		}()
		dropAllVolumeCaches()
	}()
}

func dropAllVolumeCaches() {
	// 必须和 APFS 解析串行：否则可能关掉别的 goroutine 正在读的句柄
	apfsGlobalMu.Lock()
	defer apfsGlobalMu.Unlock()
	apfsCacheMu.Lock()
	defer apfsCacheMu.Unlock()
	for k, h := range apfsCache {
		if h.dev != nil {
			h.dev.Close()
			h.dev = nil
		}
		if h.dec != nil {
			h.dec.Close()
			h.dec = nil
		}
		delete(apfsCache, k)
	}
}

// ---------------- 分区表 ----------------

func partTableOf(dev string) (map[string]any, error) {
	disks, err := scanDisks()
	if err != nil {
		return nil, err
	}
	var d *diskInfo
	for i := range disks {
		if strings.EqualFold(disks[i].Path, dev) {
			d = &disks[i]
			break
		}
	}
	if d == nil {
		return nil, fmt.Errorf("设备 %s 不存在", dev)
	}
	// 先按偏移排序，再生成列表：以前是先建列表后排序，
	// 界面上分区顺序和实际排布对不上，空隙也算错位。
	sort.Slice(d.Parts, func(i, j int) bool { return d.Parts[i].Offset < d.Parts[j].Offset })
	parts := make([]map[string]any, 0, len(d.Parts))
	for _, p := range d.Parts {
		parts = append(parts, map[string]any{
			"index": p.Index, "type": p.Type, "fs": p.Fs, "size": p.Size,
			"letter": p.Letter, "label": p.Label, "offset": p.Offset,
		})
	}
	var gaps []map[string]any
	cursor := int64(1024 * 1024)
	for _, p := range d.Parts {
		if p.Offset > cursor {
			gaps = append(gaps, map[string]any{"start_mb": cursor / (1024 * 1024), "size_mb": (p.Offset - cursor) / (1024 * 1024)})
		}
		if p.Offset+p.Size > cursor {
			cursor = p.Offset + p.Size
		}
	}
	if d.Size > cursor+16*1024*1024 {
		gaps = append(gaps, map[string]any{"start_mb": cursor / (1024 * 1024), "size_mb": (d.Size - cursor) / (1024 * 1024)})
	}
	sec := int64(512)
	if dv, err := openWinDevice(d.Path, 0, 0); err == nil {
		sec = dv.sector
		dv.Close()
	}
	return map[string]any{
		"number": d.Number, "size": d.Size, "sector": sec, "style": d.Table, "parts": parts, "gaps": gaps,
	}, nil
}

// 执行分区计划
func partExecPlan(dev, actionsJSON, confirm string) (map[string]any, error) {
	if !isAdmin() {
		return nil, fmt.Errorf("需要管理员权限")
	}
	num := diskNumberOf(dev)
	if num < 0 {
		return nil, fmt.Errorf("无法识别磁盘编号")
	}
	if err := guardDisk(dev); err != nil {
		return nil, err
	}
	if strings.TrimSpace(confirm) != fmt.Sprintf("%d", num) {
		return nil, fmt.Errorf("确认失败：磁盘编号不匹配")
	}
	var actions []map[string]any
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		return nil, fmt.Errorf("计划格式错误：%v", err)
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("计划为空")
	}
	diskOpMu.Lock()
	defer diskOpMu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("select disk %d\r\n", num))
	for _, a := range actions {
		op, _ := a["op"].(string)
		switch op {
		case "clean":
			sb.WriteString("clean\r\n")
		case "cleanall":
			sb.WriteString("clean all\r\n")
		case "convert":
			style, _ := a["style"].(string)
			if style == "" {
				style = "gpt"
			}
			sb.WriteString("convert " + style + "\r\n")
		case "create":
			size, _ := a["size_mb"].(float64)
			off, _ := a["offset_mb"].(float64)
			cmd := "create partition primary"
			if size > 0 {
				cmd += fmt.Sprintf(" size=%d", int(size))
			}
			if off > 0 {
				cmd += fmt.Sprintf(" offset=%d", int(off))
			}
			sb.WriteString(cmd + "\r\n")
			if fs, _ := a["fs"].(string); fs != "" {
				if f, ok := fsDiskpartName(fs); ok {
					quick := ""
					if q, _ := a["quick"].(bool); q {
						quick = "quick"
					}
					line := fmt.Sprintf("format fs=%s %s", f, quick)
					if label, _ := a["label"].(string); label != "" {
						line += fmt.Sprintf(" label=\"%s\"", sanitizeLabel(label))
					}
					sb.WriteString(strings.TrimSpace(line) + "\r\n")
				}
			}
			if letter, _ := a["letter"].(string); letter != "" {
				sb.WriteString("assign letter=" + strings.ToUpper(strings.TrimSuffix(letter, ":")) + "\r\n")
			}
		case "delete":
			pn, _ := a["part"].(float64)
			sb.WriteString(fmt.Sprintf("select partition %d\r\ndelete partition override\r\n", int(pn)))
		case "format":
			pn, _ := a["part"].(float64)
			fs, _ := a["fs"].(string)
			f, ok := fsDiskpartName(fs)
			if !ok {
				return nil, fmt.Errorf("Windows 不支持格式化为 %s", fs)
			}
			sb.WriteString(fmt.Sprintf("select partition %d\r\n", int(pn)))
			line := "format fs=" + f
			if q, _ := a["quick"].(bool); q {
				line += " quick"
			}
			if label, _ := a["label"].(string); label != "" {
				line += fmt.Sprintf(" label=\"%s\"", sanitizeLabel(label))
			}
			sb.WriteString(strings.TrimSpace(line) + "\r\n")
		case "shrink":
			pn, _ := a["part"].(float64)
			mb, _ := a["size_mb"].(float64)
			sb.WriteString(fmt.Sprintf("select partition %d\r\nshrink desired=%d\r\n", int(pn), int(mb)))
		case "extend":
			pn, _ := a["part"].(float64)
			sb.WriteString(fmt.Sprintf("select partition %d\r\nextend\r\n", int(pn)))
		}
	}
	out, err := runDiskpart(sb.String())
	dropAllVolumeCaches()
	code := 0
	if err != nil {
		code = 1
	}
	return map[string]any{"ok": code == 0, "code": code, "script": sb.String(), "output": out}, nil
}

// 分区类型标记
func partSetTag(dev, partNo, kind, confirm string) (map[string]any, error) {
	if !isAdmin() {
		return nil, fmt.Errorf("需要管理员权限")
	}
	num := diskNumberOf(dev)
	if strings.TrimSpace(confirm) != fmt.Sprintf("%d", num) {
		return nil, fmt.Errorf("确认失败：磁盘编号不匹配")
	}
	if err := guardDisk(dev); err != nil {
		return nil, err
	}
	guid := ""
	labelName := ""
	switch kind {
	case "apfs":
		guid, labelName = gptAPFS, "APFS"
	case "hfsplus":
		guid, labelName = gptHFS, "HFS+"
	case "recovery":
		guid, labelName = gptAppleRecovery, "Apple 恢复"
	case "efi":
		guid, labelName = gptEFI, "EFI 系统"
	case "msdata":
		guid, labelName = gptMSData, "Microsoft 数据"
	default:
		return nil, fmt.Errorf("未知类型标记：%s", kind)
	}
	if !validPartNo(partNo) {
		return nil, fmt.Errorf("分区号不合法：%q", partNo)
	}
	script := fmt.Sprintf("select disk %d\r\nselect partition %s\r\nset id=%s\r\n", num, partNo, guid)
	out, err := runDiskpart(script)
	code := 0
	if err != nil {
		code = 1
	}
	return map[string]any{"ok": code == 0, "guid": guid, "label": labelName, "output": out}, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func allDigits(s string) bool {
	if s == "" || len(s) > 12 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validPartNo(s string) bool {
	if s == "" || len(s) > 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func partFormatOne(dev, partNo, fs, label, mode, confirm string) (map[string]any, error) {
	if !isAdmin() {
		return nil, fmt.Errorf("需要管理员权限")
	}
	// 分区号直接拼进 diskpart 脚本，必须只允许数字，
	// 否则 "1\r\nclean" 这种输入能把整块盘清掉（脚本注入）。
	if !validPartNo(partNo) {
		return nil, fmt.Errorf("分区号不合法：%q", partNo)
	}
	num := diskNumberOf(dev)
	if strings.TrimSpace(confirm) != fmt.Sprintf("%d", num) {
		return nil, fmt.Errorf("确认失败：磁盘编号不匹配")
	}
	if err := guardDisk(dev); err != nil {
		return nil, err
	}
	f, ok := fsDiskpartName(fs)
	if !ok {
		return nil, fmt.Errorf("Windows 不支持格式化为 %s（可用 NTFS / exFAT / FAT32 / ReFS）", strings.ToUpper(fs))
	}
	script := fmt.Sprintf("select disk %d\r\nselect partition %s\r\n", num, partNo)
	line := "format fs=" + f
	if mode == "quick" {
		line += " quick"
	}
	if label != "" {
		line += fmt.Sprintf(" label=\"%s\"", sanitizeLabel(label))
	}
	script += line + "\r\n"
	out, err := runDiskpart(script)
	code := 0
	if err != nil {
		code = 1
	}
	dropAllVolumeCaches()
	return map[string]any{"ok": code == 0, "code": code, "script": script, "output": out}, nil
}

// ---------------- APFS 模板还原 ----------------

func apfsTemplateInfo(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开模板失败：%v", err)
	}
	defer f.Close()
	info, _ := f.Stat()
	c, err := openAPFSContainer(f, info.Size())
	if err != nil {
		return nil, fmt.Errorf("模板不是有效的 APFS 容器：%v", err)
	}
	names := []string{}
	for _, v := range c.volumes {
		names = append(names, v.Name)
	}
	return map[string]any{
		"uuid":       uuidString(c.nx.UUID),
		"block_size": c.bs,
		"capacity":   c.blockCnt * c.bs,
		"volumes":    names,
		"size":       info.Size(),
	}, nil
}

func applyAPFSTemplate(dev string, offsetMB int64, imagePath, confirm string) (*taskRec, error) {
	if !isAdmin() {
		return nil, fmt.Errorf("需要管理员权限")
	}
	num := diskNumberOf(dev)
	if strings.TrimSpace(confirm) != fmt.Sprintf("%d", num) {
		return nil, fmt.Errorf("确认失败：磁盘编号不符合")
	}
	img, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("打开模板失败：%v", err)
	}
	defer img.Close()
	st, _ := img.Stat()

	if err := guardDisk(dev); err != nil {
		return nil, err
	}
	// 模板必须先确认是合法 APFS 容器，否则可能把垃圾数据写进盘里
	if _, err := openAPFSContainer(img, st.Size()); err != nil {
		return nil, fmt.Errorf("模板不是有效的 APFS 容器：%v", err)
	}
	// 偏移 0 会盖掉分区表；写超盘尾会越界写坏后面数据 —— 都必须拦住
	if offsetMB < 1 {
		return nil, fmt.Errorf("偏移至少 1MB（0 会覆盖分区表）")
	}
	var dSize int64
	if disks, err := scanDisks(); err == nil {
		for _, dd := range disks {
			if strings.EqualFold(dd.Path, dev) {
				dSize = dd.Size
			}
		}
	}
	if dSize > 0 && offsetMB*1024*1024+st.Size() > dSize {
		return nil, fmt.Errorf("模板 + 偏移超出磁盘容量（盘 %s，需要到 %s）",
			humanBytes(dSize), humanBytes(offsetMB*1024*1024+st.Size()))
	}
	t := newTask("template", dev, "apfs", filepath.Base(imagePath), "apply")
	goSafe("format", func() {
		diskOpMu.Lock()
		defer diskOpMu.Unlock()
		offset := offsetMB * 1024 * 1024
		t.stage("写入模板镜像", 0.05)
		t.log("info", fmt.Sprintf("目标偏移 %d MB，模板 %d 字节", offsetMB, st.Size()))
		w, err := openWinDeviceRW(dev, offset, st.Size())
		if err != nil {
			t.finish(false, err.Error())
			return
		}
		defer w.Close()
		buf := make([]byte, 1<<20)
		var written int64
		for {
			n, err := img.Read(buf)
			if n > 0 {
				if _, werr := w.writeRaw(buf[:n], int64(written)); werr != nil {
					t.finish(false, "写入失败："+werr.Error())
					return
				}
				written += int64(n)
				t.stage("", 0.05+0.9*float64(written)/float64(st.Size()))
			}
			if err == io.EOF || n == 0 {
				break
			}
			if err != nil {
				t.finish(false, "读取模板失败："+err.Error())
				return
			}
		}
		t.stage("回读校验", 0.97)
		dropAPFSCache(dev, offset)
		buf2 := make([]byte, 4096)
		dev2, err2 := openWinDevice(dev, offset, st.Size())
		if err2 == nil {
			_, err2 = dev2.ReadAt(buf2, 0)
			dev2.Close()
		}
		if err2 == nil && len(buf2) > 36 && string(buf2[32:36]) == "NXSB" {
			t.log("ok", "回读校验通过：NXSB 已就位")
			t.finish(true, fmt.Sprintf("模板已写入 %.1f MB", float64(written)/1048576))
		} else {
			// 回读校验不过却报「成功」是误导：用户会以为盘已经可用
			t.finish(false, "回读校验失败：写入后没读到 NXSB，镜像可能没有完整写入")
		}
	})
	return t, nil
}

// 一键抹掉 / 一键分区（APFS）
func apfsQuickFormat(dev, sizeMB, imagePath, name, confirm string) (*taskRec, error) {
	if !isAdmin() {
		return nil, fmt.Errorf("需要管理员权限")
	}
	num := diskNumberOf(dev)
	if strings.TrimSpace(confirm) != fmt.Sprintf("%d", num) {
		return nil, fmt.Errorf("确认失败：磁盘编号不匹配")
	}
	t := newTask("apfsquick", dev, "apfs", name, "quick")
	goSafe("format", func() {
		diskOpMu.Lock()
		defer diskOpMu.Unlock()
		script := fmt.Sprintf("select disk %d\r\nclean\r\nconvert gpt\r\n", num)
		// sizeMB 直接拼进 diskpart 脚本，只允许数字（否则 "100\r\nclean" 能清盘）
		if sizeMB == "" || sizeMB == "0" {
			script += "create partition primary\r\n"
		} else if validPartNo(sizeMB) || allDigits(sizeMB) {
			script += fmt.Sprintf("create partition primary size=%s\r\n", sizeMB)
		} else {
			t.finish(false, "分区大小不合法："+sizeMB)
			return
		}
		script += fmt.Sprintf("set id=%s\r\n", gptAPFS)
		t.stage("重建 GPT + 创建 APFS 分区", 0.3)
		out, err := runDiskpart(script)
		for _, l := range strings.Split(out, "\n") {
			if s := strings.TrimSpace(strings.Trim(l, "\r")); s != "" {
				t.log("info", s)
			}
		}
		if err != nil {
			t.finish(false, "diskpart 失败："+err.Error())
			return
		}
		t.addProgress(0.3)
		if imagePath != "" {
			t.stage("写入 APFS 模板", 0.6)
			// 刚改过分区表，必须让磁盘信息缓存失效再读，否则拿到的是旧布局，
			// 有可能把模板写进别的分区（覆盖别人的数据）。
			invalidateScanCache()
			disks, _ := scanDisks()
			var target *partInfo
			for _, d := range disks {
				if !strings.EqualFold(d.Path, dev) {
					continue
				}
				for i := range d.Parts {
					p := &d.Parts[i]
					// 新建的分区一定在最末尾（偏移最大）
					if target == nil || p.Offset > target.Offset {
						target = p
					}
				}
			}
			if target == nil {
				t.finish(false, "创建分区后没读到新分区，模板未写入")
				return
			}
			img, err := os.Open(imagePath)
			if err != nil {
				t.finish(false, "打开模板失败："+err.Error())
				return
			}
			ist, _ := img.Stat()
			if ist.Size() > target.Size {
				img.Close()
				t.finish(false, fmt.Sprintf("模板 %s 比分区 %s 大，写不进去", humanBytes(ist.Size()), humanBytes(target.Size)))
				return
			}
			w, werr := openWinDeviceRW(dev, target.Offset, target.Size)
			if werr != nil {
				img.Close()
				t.finish(false, "打开分区写入失败："+werr.Error())
				return
			}
			_, cerr := io.Copy(&rawWriter{w: w}, img)
			w.Close()
			img.Close()
			if cerr != nil {
				t.finish(false, "写入模板失败："+cerr.Error())
				return
			}
			t.log("ok", fmt.Sprintf("模板已写入分区起始处（偏移 %s），Mac 可直接识别", humanBytes(target.Offset)))
			t.addProgress(0.3)
		} else {
			t.log("info", "未提供 APFS 模板：分区已标记为 APFS 类型，到 Mac 上抹掉即可")
		}
		dropAllVolumeCaches()
		t.finish(true, "一键 APFS 分区完成")
	})
	return t, nil
}

type rawWriter struct {
	w   *winDevice
	off int64
}

func (r *rawWriter) Write(p []byte) (int, error) {
	n, err := r.w.writeRaw(p, r.off)
	r.off += int64(n)
	return n, err
}

var _ = bytes.MinRead
var _ = windows.CloseHandle
var _ = types.BLOCK_SIZE
