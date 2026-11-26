//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blacktop/go-apfs/types"
)

// ---------------------------------------------------------------------------
// 容器缓存：同一块盘 / 同一分区只解析一次
// ---------------------------------------------------------------------------

type apfsHandle struct {
	dev    *winDevice
	c      *apfsContainer
	key    string
	last   int64
	unlock *unlockState
	dec      io.Closer
	unlocked bool
	unlockPass string
}

func nowUnix() int64 { return time.Now().Unix() }

type unlockState struct {
	mu     sync.Mutex
	veks   map[uint64][]byte // fs_oid -> VEK
	diag   string
	pass   string
}

var (
	apfsCacheMu sync.Mutex
	apfsCache   = map[string]*apfsHandle{}
)

func apfsKey(dev string, offset int64) string {
	return fmt.Sprintf("%s@%d", dev, offset)
}

func withAPFS(dev string, offset int64, size int64, oidHint uint64, fn func(c *apfsContainer) error) error {
	apfsGlobalMu.Lock()
	defer apfsGlobalMu.Unlock()

	key := apfsKey(dev, offset)
	apfsCacheMu.Lock()
	h, ok := apfsCache[key]
	if ok && h.c == nil {
		ok = false
	}
	apfsCacheMu.Unlock()

	if !ok {
		d, err := openWinDevice(dev, offset, size)
		if err != nil {
			return err
		}
		c, err := openContainerLocked(d, size)
		if err != nil {
			d.Close()
			return err
		}
		h = &apfsHandle{dev: d, c: c, key: key, last: time.Now().Unix()}
		apfsCacheMu.Lock()
		if old, exists := apfsCache[key]; exists && old.dev != nil {
			old.dev.Close()
		}
		apfsCache[key] = h
		// 限制缓存数量：真正淘汰最久未用的那一份（旧版这里是个空转循环，
		// 句柄只增不减，长时间使用会累积一堆句柄）
		for len(apfsCache) > 3 {
			oldestKey := ""
			oldest := int64(1 << 62)
			for k, v := range apfsCache {
				if k == key {
					continue
				}
				if v.last < oldest {
					oldest, oldestKey = v.last, k
				}
			}
			if oldestKey == "" {
				break
			}
			if oh := apfsCache[oldestKey]; oh != nil {
				if oh.dev != nil {
					oh.dev.Close()
				}
				if oh.dec != nil {
					oh.dec.Close()
				}
			}
			delete(apfsCache, oldestKey)
		}
		apfsCacheMu.Unlock()
	}
	apfsCacheMu.Lock()
	h.last = time.Now().Unix()
	apfsCacheMu.Unlock()
	return fn(h.c)
}

func dropAPFSCache(dev string, offset int64) {
	apfsGlobalMu.Lock()
	defer apfsGlobalMu.Unlock()
	key := apfsKey(dev, offset)
	apfsCacheMu.Lock()
	if h, ok := apfsCache[key]; ok {
		if h.dev != nil {
			h.dev.Close()
		}
		if h.dec != nil {
			h.dec.Close()
		}
		delete(apfsCache, key)
	}
	apfsCacheMu.Unlock()
}

// ---------------------------------------------------------------------------
// 分区 / 容器扫描
// ---------------------------------------------------------------------------

type apfsVolView struct {
	Dev        string `json:"dev"`
	Offset     int64  `json:"offset"`
	FsOid      uint64 `json:"fs_oid"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	UUID       string `json:"uuid"`
	Group      string `json:"volume_group"`
	Encrypted  bool   `json:"encrypted"`
	Unlocked   bool   `json:"unlocked"`
	Capacity   uint64 `json:"capacity"`
	Used       uint64 `json:"used"`
	Sealed     bool   `json:"sealed"`
	CaseInsens bool   `json:"case_insensitive"`
	OpenErr    string `json:"open_err,omitempty"`
}

type partSpan struct{ Off, Size int64 }

// apfsSpans 得到「值得一试」的容器候选范围，来源优先级：
// 1) 自己从原始 LBA 解析的 GPT / MBR / APM（最可信，绕开 Windows）；
// 2) Windows 的 Get-Partition；
// 3) 老办法：整盘从 0 开始。
func apfsSpans(d diskInfo) ([]partSpan, string) {
	if parts, table, err := parsePartitionTable(d.Path); err == nil && len(parts) > 0 {
		var out []partSpan
		for _, p := range parts {
			if p.Size <= 0 {
				continue
			}
			out = append(out, partSpan{Off: p.Off, Size: p.Size})
		}
		if len(out) > 0 {
			return out, "自解析 " + table
		}
	}
	if len(d.Parts) > 0 {
		var out []partSpan
		for _, p := range d.Parts {
			if p.Size <= 0 {
				continue
			}
			out = append(out, partSpan{Off: p.Offset, Size: p.Size})
		}
		if len(out) > 0 {
			return out, "Windows 分区表"
		}
	}
	return []partSpan{{0, d.Size}}, "整盘（没有分区表信息）"
}

func apfsPartitions(d diskInfo) []struct{ Off, Size int64 } {
	spans, _ := apfsSpans(d)
	out := make([]struct{ Off, Size int64 }, 0, len(spans))
	for _, s := range spans {
		out = append(out, struct{ Off, Size int64 }{s.Off, s.Size})
	}
	return out
}

// scanNXSBOffsets 在磁盘前 limit 字节内按 4K 步长找 APFS 容器魔数。
// APFS 容器块 0 的偏移 32 处固定是 "NXSB"，加密容器的块 0 也是明文。
func scanNXSBOffsets(dev string, limit int64) []int64 {
	d, err := openWinDevice(dev, 0, 0)
	if err != nil {
		return nil
	}
	defer d.Close()
	buf := make([]byte, 4096)
	var out []int64
	// 细扫：前 8MB，4K 步长（容器一般就在开头）
	for off := int64(0); off < 8<<20; off += 4096 {
		n, rerr := d.readRaw(buf, off)
		if rerr != nil || n < 36 {
			continue
		}
		if string(buf[32:36]) == "NXSB" {
			out = append(out, off)
			if len(out) >= 4 {
				return out
			}
		}
	}
	// 粗扫：8MB ~ limit，1MB 步长。分区一般按 MB 对齐，
	// 这样即使 Windows 读不到分区表也能把容器位置捞出来。
	if limit <= 8<<20 {
		limit = 512 << 20
	}
	for off := int64(8 << 20); off < limit; off += 1 << 20 {
		n, rerr := d.readRaw(buf, off)
		if rerr != nil || n < 36 {
			continue
		}
		if string(buf[32:36]) == "NXSB" {
			out = append(out, off)
			if len(out) >= 4 {
				break
			}
		}
	}
	return out
}

// 扫描某个设备上的 APFS 分区与卷
func scanAPFSVolumes(dev string) ([]apfsVolView, map[string]string, error) {
	disks, err := scanDisks()
	if err != nil {
		return nil, nil, err
	}
	var target *diskInfo
	for i := range disks {
		if strings.EqualFold(disks[i].Path, dev) {
			target = &disks[i]
			break
		}
	}
	if target == nil {
		return nil, nil, fmt.Errorf("设备 %s 不存在", dev)
	}
	diag := map[string]string{}
	var vols []apfsVolView
	spans, src := apfsSpans(*target)
	diag["分区表来源"] = src
	if d, err := openWinDevice(dev, 0, 0); err == nil {
		diag["物理扇区大小"] = fmt.Sprintf("%d 字节", d.sector)
		d.Close()
	} else {
		diag["打开设备"] = err.Error()
	}
	diag["管理员"] = fmt.Sprintf("%v", isAdmin())
	for _, p := range spans {
		func() {
			defer func() {
				if r := recover(); r != nil {
					diag[fmt.Sprintf("offset %d", p.Off)] = fmt.Sprintf("解析 panic：%v", r)
				}
			}()
			err := withAPFS(dev, p.Off, p.Size, 0, func(c *apfsContainer) error {
				for _, v := range c.volumes {
					vols = append(vols, apfsVolView{
						Dev: dev, Offset: p.Off, FsOid: uint64(v.oid), Name: v.Name, Role: v.Role,
						UUID: v.UUID, Group: v.GroupID, Capacity: v.Capacity, Used: v.Used,
						Sealed: v.Sealed, CaseInsens: v.CaseInsensitive, Encrypted: v.Encrypted && !v.Usable,
						OpenErr: v.OpenErr, Unlocked: false,
					})
				}
				for _, lv := range c.locked {
					vols = append(vols, apfsVolView{
						Dev: dev, Offset: p.Off, FsOid: uint64(lv.Oid), Name: lv.Name, Role: lv.Role,
						UUID: lv.UUID, Group: lv.GroupID, Capacity: lv.Capacity, Used: lv.Used,
						Encrypted: true, Unlocked: false,
					})
				}
				diag[fmt.Sprintf("卷数量 @%d", p.Off)] = fmt.Sprintf("%d 个可读 / %d 个加密", len(c.volumes), len(c.locked))
				if len(c.volumes) == 0 && len(c.locked) == 0 {
					// 容器能打开但一个卷都没有：把内部注记带出来，用户看得到卡在哪
					note := "容器已解析，但没找到任何卷"
					if len(c.notes) > 0 {
						note += "：" + strings.Join(c.notes, "；")
					}
					diag[fmt.Sprintf("卷为空 @%d", p.Off)] = note
				}
				return nil
			})
			if err != nil {
				diag[fmt.Sprintf("分区 @%d", p.Off)] = err.Error()
			}
		}()
	}
	if len(vols) == 0 {
		// 前面按分区偏移都没解析出来，再做一次「盲扫」：
		// 在磁盘前 16MB 以 4K 步长找 NXSB 魔数。这样即使 Windows 读不到
		// 分区表（APM/MBR 异常、分区偏移不准），也能把容器捞回来。
		if cand := scanNXSBOffsets(dev, 512<<20); len(cand) > 0 {
			for _, off := range cand {
				seg := func() {
					defer func() {
						if r := recover(); r != nil {
							diag[fmt.Sprintf("盲扫 @%d", off)] = fmt.Sprintf("解析 panic：%v", r)
						}
					}()
					err := withAPFS(dev, off, 0, 0, func(c *apfsContainer) error {
						for _, v := range c.volumes {
							vols = append(vols, apfsVolView{
								Dev: dev, Offset: off, FsOid: uint64(v.oid), Name: v.Name, Role: v.Role,
								UUID: v.UUID, Group: v.GroupID, Capacity: v.Capacity, Used: v.Used,
								Sealed: v.Sealed, CaseInsens: v.CaseInsensitive, Encrypted: v.Encrypted && !v.Usable,
								OpenErr: v.OpenErr,
							})
						}
						for _, lv := range c.locked {
							vols = append(vols, apfsVolView{
								Dev: dev, Offset: off, FsOid: uint64(lv.Oid), Name: lv.Name, Role: lv.Role,
								UUID: lv.UUID, Group: lv.GroupID, Capacity: lv.Capacity, Used: lv.Used,
								Encrypted: true,
							})
						}
						return nil
					})
					if err != nil {
						diag[fmt.Sprintf("盲扫 @%d", off)] = err.Error()
					} else {
						diag[fmt.Sprintf("盲扫 @%d", off)] = "解析成功（分区表可能未被 Windows 正确识别）"
					}
				}
				seg()
			}
		}
	}
	if len(vols) == 0 && len(diag) == 0 {
		diag["结果"] = "没有发现 APFS 容器"
	}
	return vols, diag, nil
}

func volumeViewFields(v *apfsVolume) map[string]any {
	return map[string]any{
		"name": v.Name, "role": v.Role, "uuid": v.UUID, "volume_group": v.GroupID,
		"capacity": v.Capacity, "used": v.Used, "free": int64(v.Capacity) - int64(v.Used),
		"block_size": v.c.bs, "files": v.Files, "dirs": v.Dirs, "snapshots": v.Snaps,
		"symlinks": v.Symlinks, "other_objects": v.Others,
		"sealed": v.Sealed, "case_insensitive": v.CaseInsensitive, "encrypted": v.Encrypted, "unlocked": false,
	}
}

// ---------------------------------------------------------------------------
// 目录 / 文件读取辅助
// ---------------------------------------------------------------------------

func findVolume(c *apfsContainer, oid uint64) (*apfsVolume, error) {
	if v := c.findVolume(types.OidT(oid)); v != nil {
		return v, nil
	}
	if oid == 0 && len(c.volumes) > 0 {
		return c.volumes[0], nil
	}
	return nil, fmt.Errorf("找不到卷 %#x", oid)
}

func parseOid(s string) uint64 {
	var n uint64
	fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	if n == 0 {
		var hexN uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%x", &hexN); err == nil {
			n = hexN
		}
	}
	return n
}

func parseSnap(s string) uint64 { return parseOid(s) }

// 递归遍历（用于搜索 / 大文件 / 空间统计）
type walkEntry struct {
	Path  string
	Size  uint64
	Mtime int64
	IsDir bool
}

func walkVolume(v *apfsVolume, rootPath string, snap uint64, maxEntries int, maxDepth int, fn func(e walkEntry) bool) error {
	omap, fsRoot, err := v.view(snap)
	if err != nil {
		return err
	}
	var count int
	var walk func(path string, oid types.OidT, depth int) bool
	visited := map[types.OidT]bool{}
	walk = func(path string, oid types.OidT, depth int) bool {
		// maxEntries 以前只传不用，大卷会无限递归遍历；
		// 现在真的生效，避免后台任务长时间占着全局锁把界面拖死。
		if depth > maxDepth || visited[oid] || (maxEntries > 0 && count >= maxEntries) {
			return true
		}
		visited[oid] = true
		recs, err := v.recordsWith(omap, fsRoot, oid, snap)
		if err != nil {
			return true
		}
		for _, r := range recs {
			if r.Hdr.GetType() != types.APFS_TYPE_DIR_REC {
				continue
			}
			k, ok := r.Key.(types.JDrecHashedKeyT)
			if !ok {
				continue
			}
			name := cleanDirName(k.Name)
			if name == "" || name == "." || name == ".." {
				continue
			}
			// 隐藏项（. 开头）在搜索/大文件/快照对比里同样不返回，
			// 否则用户会在结果里看到一堆 .DS_Store、._xxx。
			if strings.HasPrefix(name, ".") {
				continue
			}
			val, okv := r.Val.(types.JDrecVal)
			if !okv {
				continue
			}
			childOid := types.OidT(val.FileID)
			isDir := entryType(uint16(val.Flags)) == "dir"
			childPath := path
			if childPath == "/" {
				childPath += name
			} else {
				childPath += "/" + name
			}
			var size uint64
			var mtime int64
			if ir, err := v.recordsWith(omap, fsRoot, childOid, snap); err == nil {
				for _, rec := range ir {
					if rec.Hdr.GetType() != types.APFS_TYPE_INODE {
						continue
					}
					in, ok := rec.Val.(types.JInodeVal)
					if !ok {
						continue
					}
					mtime = apfsTime(int64(in.ModTime))
					for _, xf := range in.Xfields {
						if xf.XType == types.INO_EXT_TYPE_DSTREAM {
							if ds, ok := xf.Field.(types.JDstreamT); ok {
								size = ds.Size
							}
						}
					}
					if in.InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 && in.UncompressedSize > 0 {
						size = in.UncompressedSize
					}
					break
				}
			}
			count++
			if !fn(walkEntry{Path: childPath, Size: size, Mtime: mtime, IsDir: isDir}) {
				return false
			}
			if maxEntries > 0 && count >= maxEntries {
				return false
			}
			if isDir {
				if !walk(childPath, childOid, depth+1) {
					return false
				}
			}
		}
		return true
	}
	if !walk(rootPath, types.OidT(types.FSROOT_OID), 0) {
		return nil
	}
	return nil
}

func joinPath(base, name string) string {
	if base == "" || base == "/" {
		return "/" + strings.TrimPrefix(name, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(name, "/")
}

func safeFileName(s string) string {
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "*", "_")
	s = strings.ReplaceAll(s, "?", "_")
	s = strings.ReplaceAll(s, "\"", "_")
	s = strings.ReplaceAll(s, "<", "_")
	s = strings.ReplaceAll(s, ">", "_")
	s = strings.ReplaceAll(s, "|", "_")
	s = strings.TrimSpace(s)
	if s == "" {
		s = "untitled"
	}
	return s
}

func downloadsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func appDataDir() string {
	d := filepath.Join(os.Getenv("LOCALAPPDATA"), "UDiskTool")
	if os.Getenv("LOCALAPPDATA") == "" {
		d = filepath.Join(os.TempDir(), "UDiskTool")
	}
	_ = os.MkdirAll(d, 0o755)
	return d
}

var errUnsupported = errors.New("不支持的操作")
var _ = io.Discard
var _ = sort.Strings
