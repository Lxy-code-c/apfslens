//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
	"unsafe"
)

var (
	shell32                = windows.NewLazySystemDLL("shell32.dll")
	procSHChangeNotify     = shell32.NewProc("SHChangeNotify")
	procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
)

type mountRec struct {
	ID     string `json:"id"`
	Volume string `json:"volume"`
	Device string `json:"device"`
	Offset int64  `json:"offset"`
	FsOid  uint64 `json:"fs_oid"`
	Snap   string `json:"snap,omitempty"`
	Letter string `json:"letter"`
	Dir    string `json:"dir"`
	Auto   bool   `json:"auto"`
	At     int64  `json:"at"`
	State  string `json:"state"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Total  int    `json:"total"`
	Err    string `json:"error,omitempty"`
	RO     bool   `json:"ro"`
	dead   bool   // 已被卸载：后台复制循环看到就立刻退出（json 不导出）
}

var (
	mountMu   sync.Mutex
	mountRecs = map[string]*mountRec{}
	_         struct{} // 占位，避免空 var 块
)

func prefsPath() string {
	return filepath.Join(appDataDir(), "prefs.json")
}

type prefsFile struct {
	Mounts []*mountRec `json:"mounts"`
}

func loadPrefs() prefsFile {
	var p prefsFile
	b, err := os.ReadFile(prefsPath())
	if err != nil {
		return p
	}
	_ = json.Unmarshal(b, &p)
	return p
}

var prefsMu sync.Mutex

func savePrefs() {
	// 快照在锁内完成：后台同步会持续改写 Files/Bytes/State，
	// 在锁外直接序列化指针属于数据竞争，写出的 json 可能半新半旧。
	mountMu.Lock()
	snap := make([]mountRec, 0, len(mountRecs))
	for _, r := range mountRecs {
		snap = append(snap, *r)
	}
	mountMu.Unlock()
	sort.Slice(snap, func(i, j int) bool { return snap[i].At < snap[j].At })
	list := make([]*mountRec, 0, len(snap))
	for i := range snap {
		list = append(list, &snap[i])
	}
	b, _ := json.MarshalIndent(prefsFile{Mounts: list}, "", "  ")
	// 多个 goroutine 会同时保存（挂载/卸载/后台同步），
	// 并发写同一个文件会把 json 写成半截 —— 加锁串行化。
	prefsMu.Lock()
	defer prefsMu.Unlock()
	_ = os.MkdirAll(appDataDir(), 0o755)
	_ = os.WriteFile(prefsPath(), b, 0o644)
}

func mountDir(id string) string {
	return filepath.Join(appDataDir(), "mounts", id)
}

// freeSpaceOf 查本机磁盘剩余空间（镜像目录所在卷）。
func freeSpaceOf(dir string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return int64(free), true
}

func driveLetterFree(letter string) bool {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return false
	}
	if letter == "" || len(letter) != 1 {
		return false
	}
	i := int(letter[0] - 'A')
	if i < 0 || i > 25 {
		return false
	}
	return mask&(1<<uint(i)) == 0
}

func pickLetter() string {
	for c := byte('E'); c <= 'Z'; c++ {
		if driveLetterFree(string(c)) {
			return string(c)
		}
	}
	return ""
}

func normLetter(s string) string {
	s = strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ":")))
	if len(s) == 1 && s[0] >= 'A' && s[0] <= 'Z' {
		return s
	}
	return ""
}

// driveVisible 检查盘符是否真的能被系统识别（不是「调用没报错」就算成功）。
func driveVisible(letter string) bool {
	root, err := windows.UTF16PtrFromString(letter + ":\\")
	if err != nil {
		return false
	}
	return windows.GetDriveType(root) != windows.DRIVE_NO_ROOT_DIR
}

// mapDrive 把本地目录映射成盘符（等效 subst，无需驱动）。
// 先用 DefineDosDevice；不可见时退回 subst 再试一次 —— 不同 Windows 版本
// 对「管理员会话里创建的盘符」可见性不一致，多一条路成功率高得多。
func mapDrive(letter, dir string) error {
	var first error
	dev, err := windows.UTF16PtrFromString(letter + ":")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString("\\??\\" + dir)
	if err != nil {
		return err
	}
	if err := windows.DefineDosDevice(windows.DDD_RAW_TARGET_PATH, dev, target); err != nil {
		first = err
	} else if driveVisible(letter) {
		notifyDrive(letter, true)
		return nil
	}

	// 回退 1：走 subst（同一套 DOS 设备表，但走的是系统自带实现）
	out, serr := runHidden(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"),
		"/c", "subst", letter+":", dir)
	if serr == nil && driveVisible(letter) {
		notifyDrive(letter, true)
		return nil
	}
	// 回退 2：不带 RAW 标志再试一次（直接给路径，让系统自己补 \??\）
	plain, perr := windows.UTF16PtrFromString(dir)
	if perr == nil {
		if err2 := windows.DefineDosDevice(0, dev, plain); err2 == nil && driveVisible(letter) {
			notifyDrive(letter, true)
			return nil
		}
	}
	msg := fmt.Sprintf("盘符 %s: 没有出现在系统里", letter)
	if first != nil {
		msg += fmt.Sprintf("（DefineDosDevice：%v）", first)
	}
	if serr != nil {
		msg += fmt.Sprintf("（subst：%v %s）", serr, strings.TrimSpace(out))
	}
	if isAdmin() {
		msg += "。当前是管理员权限运行，Windows 有时不把管理员会话里的盘符显示给资源管理器：请用普通权限再启动一次（或直接打开镜像目录浏览）"
	}
	return fmt.Errorf("%s", msg)
}

// notifyDrive 广播驱动器变化，让「此电脑」立刻出现/消失盘符。
func notifyDrive(letter string, add bool) {
	const (
		shcneDriveAdd     = 0x00000100
		shcneDriveRemoved = 0x00000400
		shcnfPathW        = 0x0005
		wmDeviceChange    = 0x0219
		dbtDriveAdded     = 0x8002
		dbtDriveRemoved   = 0x8004
	)
	ev := shcneDriveRemoved
	dbt := dbtDriveRemoved
	if add {
		ev = shcneDriveAdd
		dbt = dbtDriveAdded
	}
	if p := letter + ":\\"; true {
		path, err := windows.UTF16PtrFromString(p)
		if err == nil {
			procSHChangeNotify.Call(uintptr(ev), shcnfPathW, uintptr(unsafe.Pointer(path)), 0)
		}
	}
	wparam := uintptr(1) << (uint(letter[0]-'A') + 8)
	procSendMessageTimeout.Call(0xffff, wmDeviceChange, wparam, uintptr(dbt), 0x0002, 500, 0)
}

func unmapDrive(letter, dir string) error {
	if letter == "" {
		return nil
	}
	dev, err := windows.UTF16PtrFromString(letter + ":")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString("\\??\\" + dir)
	if err != nil {
		return err
	}
	if err := windows.DefineDosDevice(
		windows.DDD_RAW_TARGET_PATH|windows.DDD_REMOVE_DEFINITION|windows.DDD_EXACT_MATCH_ON_REMOVE,
		dev, target); err != nil {
		// 可能是 subst 建出来的，用 subst /d 收掉
		_, _ = runHidden(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"),
			"/c", "subst", letter+":", "/d")
	}
	if driveVisible(letter) {
		_, _ = runHidden(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"),
			"/c", "subst", letter+":", "/d")
	}
	notifyDrive(letter, false)
	return nil
}

var mountSeqV int64

func newMountID() string {
	n := atomic.AddInt64(&mountSeqV, 1)
	return fmt.Sprintf("m%d_%d", time.Now().UnixNano()/1e6, n)
}

// mountVolume 立即给出盘符，后台把文件渐进复制到镜像目录。
func mountVolume(dev string, offset int64, oid uint64, letter, snap string) (*mountRec, error) {
	letter = normLetter(letter)
	if letter == "" {
		letter = pickLetter()
	}
	if letter == "" {
		return nil, fmt.Errorf("没有空闲盘符，请先在 3 点菜单里释放一个")
	}

	var volName string
	var total int
	var topDirs, topFiles []string
	err := withAPFS(dev, offset, 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		volName = v.Name
		entries, err := v.listDir("/", parseSnap(snap))
		if err == nil {
			total = len(entries)
			for _, e := range entries {
				if e.Type == "dir" {
					topDirs = append(topDirs, e.Name)
				} else {
					topFiles = append(topFiles, e.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	id := newMountID()
	dir := mountDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建镜像目录失败：%v", err)
	}
	rec := &mountRec{
		ID: id, Volume: volName, Device: dev, Offset: offset, FsOid: oid, Snap: snap,
		Letter: letter, Dir: dir, At: time.Now().Unix(), State: "syncing", RO: true, Total: total,
	}
	if err := mapDrive(letter, dir); err != nil {
		return nil, fmt.Errorf("分配盘符 %s: 失败：%v", letter, err)
	}
	// 先把顶层目录建出来，盘符打开立刻有内容，不用等后台复制
	for _, d := range topDirs {
		_ = os.MkdirAll(filepath.Join(dir, safeFileName(d)), 0o755)
	}
	_ = topFiles
	mountMu.Lock()
	mountRecs[id] = rec
	mountMu.Unlock()
	savePrefs()

	goSafe("mount-sync", func() { syncMount(rec) })
	return rec, nil
}

func syncMount(rec *mountRec) {
	// 先一次性拿到完整清单，再逐个文件复制：每步只占一次短锁，
	// 避免后台复制把接口调用全部堵住（否则切页面会像卡死）。
	var files []string
	var dirs []string
	err := withAPFS(rec.Device, rec.Offset, 0, rec.FsOid, func(c *apfsContainer) error {
		v, err := findVolume(c, rec.FsOid)
		if err != nil {
			return err
		}
		if rec.Volume == "" {
			mountMu.Lock()
			rec.Volume = v.Name
			mountMu.Unlock()
		}
		return walkVolume(v, "/", parseSnap(rec.Snap), 800000, 48, func(e walkEntry) bool {
			if e.IsDir {
				dirs = append(dirs, e.Path)
			} else {
				files = append(files, e.Path)
			}
			return true
		})
	})
	if err != nil {
		mountMu.Lock()
		rec.State = "error"
		rec.Err = err.Error()
		mountMu.Unlock()
		savePrefs()
		return
	}
	for _, d := range dirs {
		_ = os.MkdirAll(filepath.Join(rec.Dir, safeFileName(strings.TrimPrefix(d, "/"))), 0o755)
	}
	total := len(files)
	mountMu.Lock()
	rec.Total = total
	mountMu.Unlock()
	usedMu := &sync.Mutex{}
	usedDst := map[string]int{}
	failed := 0
	for i, f := range files {
		mountMu.Lock()
		dead := rec.dead
		mountMu.Unlock()
		if dead {
			return
		}
		// 镜像复制会真的占本机磁盘。剩余空间不足就先停下并说明，
		// 不能把用户 C 盘塞满（以前没有任何保护）。
		if i%8 == 0 {
			if free, ok := freeSpaceOf(rec.Dir); ok && free < 1<<30 {
				mountMu.Lock()
				rec.State = "partial"
				rec.Err = fmt.Sprintf("本机剩余空间不足 1GB，已停止复制（已复制 %d 个文件）", rec.Files)
				mountMu.Unlock()
				savePrefs()
				return
			}
		}
		rel := strings.TrimPrefix(f, "/")
		parts := strings.Split(rel, "/")
		for j := range parts {
			parts[j] = safeFileName(parts[j])
		}
		// 两个不同的源名字清洗后可能撞成同一个本地名（例如 "a:b" 与 "a_b"），
		// 直接写会互相覆盖、悄悄丢文件，所以撞了就加后缀。
		base := filepath.Join(rec.Dir, filepath.Join(parts...))
		dst := base
		usedMu.Lock()
		if n, dup := usedDst[dst]; dup {
			ext := filepath.Ext(base)
			dst = strings.TrimSuffix(base, ext) + fmt.Sprintf("_%d", n+1) + ext
			usedDst[dst] = 1
			usedDst[base] = n + 1
		} else {
			usedDst[dst] = 1
		}
		usedMu.Unlock()
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		// 同名旧文件可能是上一轮复制留下的 0o444 只读文件，先删掉再写
		_ = os.Remove(dst)
		one := func(c *apfsContainer) error {
			v, err := findVolume(c, rec.FsOid)
			if err != nil {
				return err
			}
			// 先写临时名，写完再改名：否则资源管理器里会看到「半截」的文件，
			// 打开视频/图片会报损坏。用户体验上这一步很关键。
			n, rerr := copyFileTo(v, f, parseSnap(rec.Snap), rec.Device, rec.Offset, dst)
			if rerr != nil {
				return rerr
			}
			mountMu.Lock()
			rec.Files++
			rec.Bytes += n
			mountMu.Unlock()
			_ = os.Chmod(dst, 0o444)
			return nil
		}
		if err := withAPFS(rec.Device, rec.Offset, 0, rec.FsOid, one); err != nil {
			failed++
			continue
		}
		if i%40 == 0 {
			mountMu.Lock()
			rec.Err = ""
			mountMu.Unlock()
		}
	}
	mountMu.Lock()
	if failed > 0 {
		rec.State = "partial"
		rec.Err = fmt.Sprintf("%d 个文件复制失败（已成功 %d 个）", failed, rec.Files)
	} else {
		rec.State = "ready"
		rec.Err = ""
	}
	mountMu.Unlock()
	savePrefs()
}

func copyTree(v *apfsVolume, srcDir, dstDir string, snap uint64, rec *mountRec) error {
	entries, err := v.listDir(srcDir, snap)
	if err != nil {
		return err
	}
	for _, e := range entries {
		dst := filepath.Join(dstDir, safeFileName(e.Name))
		switch e.Type {
		case "dir":
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			if err := copyTree(v, joinPath(srcDir, e.Name), dst, snap, rec); err != nil {
				continue
			}
		case "file":
			f, err := os.Create(dst)
			if err != nil {
				continue
			}
			n, err := v.readFile(joinPath(srcDir, e.Name), snap, f)
			f.Close()
			if err != nil {
				os.Remove(dst)
				continue
			}
			mountMu.Lock()
			rec.Files++
			rec.Bytes += n
			mountMu.Unlock()
			_ = os.Chmod(dst, 0o444)
		}
	}
	return nil
}

func listMounts() []*mountRec {
	mountMu.Lock()
	defer mountMu.Unlock()
	snap := make([]mountRec, 0, len(mountRecs))
	for _, r := range mountRecs {
		snap = append(snap, *r)
	}
	sort.Slice(snap, func(i, j int) bool { return snap[i].At < snap[j].At })
	out := make([]*mountRec, 0, len(snap))
	for i := range snap {
		out = append(out, &snap[i])
	}
	return out
}

func unmountVolume(id string) error {
	mountMu.Lock()
	rec, ok := mountRecs[id]
	if !ok {
		mountMu.Unlock()
		return fmt.Errorf("挂载记录不存在")
	}
	rec.dead = true
	delete(mountRecs, id)
	mountMu.Unlock()
	if rec.Letter != "" {
		_ = unmapDrive(rec.Letter, rec.Dir)
	}
	_ = os.RemoveAll(rec.Dir)
	savePrefs()
	return nil
}

// mountSet 3 点菜单：改盘符 / 开关自动挂载 / 重建缓存
func mountSet(id, letter string, auto *bool, rebuild bool) (*mountRec, error) {
	mountMu.Lock()
	rec, ok := mountRecs[id]
	if !ok {
		mountMu.Unlock()
		return nil, fmt.Errorf("挂载记录不存在")
	}
	mountMu.Unlock()

	if letter != "" {
		nl := normLetter(letter)
		if nl == "" {
			return nil, fmt.Errorf("盘符格式应为 E:")
		}
		if nl != rec.Letter {
			if !driveLetterFree(nl) {
				return nil, fmt.Errorf("盘符 %s: 已被占用", nl)
			}
			// 旧盘符必须先成功解绑，否则同一目录会同时挂在两个盘符上
			if err := unmapDrive(rec.Letter, rec.Dir); err != nil {
				return nil, fmt.Errorf("解绑旧盘符 %s: 失败：%v", rec.Letter, err)
			}
			if err := mapDrive(nl, rec.Dir); err != nil {
				_ = mapDrive(rec.Letter, rec.Dir)
				return nil, fmt.Errorf("改盘符失败：%v", err)
			}
			mountMu.Lock()
			rec.Letter = nl
			mountMu.Unlock()
		}
	}
	if auto != nil {
		mountMu.Lock()
		rec.Auto = *auto
		mountMu.Unlock()
	}
	if rebuild {
		_ = os.RemoveAll(rec.Dir)
		_ = os.MkdirAll(rec.Dir, 0o755)
		mountMu.Lock()
		rec.Files, rec.Bytes, rec.Total, rec.Err, rec.State = 0, 0, 0, "", "syncing"
		rec.dead = false // 重建缓存要让之前被卸载打断的循环重新可用
		mountMu.Unlock()
		goSafe("mount-sync", func() { syncMount(rec) })
	}
	savePrefs()
	return rec, nil
}

// autoMountAll 开机自动挂载
func autoMountAll() {
	p := loadPrefs()
	for _, cfg := range p.Mounts {
		if !cfg.Auto {
			continue
		}
		// 已经挂着的同一个卷不再挂一遍（否则会出现两个盘符指向同一份数据）
		mountMu.Lock()
		already := false
		for _, r := range mountRecs {
			if r.Device == cfg.Device && r.Offset == cfg.Offset && r.FsOid == cfg.FsOid {
				already = true
				break
			}
		}
		mountMu.Unlock()
		if already {
			continue
		}
		_, err := mountVolume(cfg.Device, cfg.Offset, cfg.FsOid, cfg.Letter, cfg.Snap)
		if err == nil {
			continue
		}
		// 盘符被占用时换一个
		if rec, err2 := mountVolume(cfg.Device, cfg.Offset, cfg.FsOid, "", cfg.Snap); err2 == nil {
			mountMu.Lock()
			rec.Auto = true
			mountMu.Unlock()
			savePrefs()
		}
	}
}

// exportPath 把 APFS 里的一个路径（文件或目录）导出到挂载镜像目录。
// 分两段：先短锁收集清单，再锁外逐个写文件，避免长时间占着全局锁。
func exportPath(rec *mountRec, srcPath string) error {
	type item struct {
		Abs string
		Rel string
	}
	var dirs []string
	var files []item
	dstRoot := rec.Dir
	err := withAPFS(rec.Device, rec.Offset, 0, rec.FsOid, func(c *apfsContainer) error {
		v, err := findVolume(c, rec.FsOid)
		if err != nil {
			return err
		}
		mountMu.Lock()
		rec.Volume = v.Name
		mountMu.Unlock()
		if srcPath == "" {
			srcPath = "/"
		}
		st, err := v.stat(srcPath, parseSnap(rec.Snap))
		if err != nil {
			return err
		}
		base := "/"
		if srcPath != "/" {
			base = strings.TrimSuffix(srcPath, "/")
		}
		if st.Type == "dir" {
			if srcPath != "/" {
				dstRoot = filepath.Join(rec.Dir, safeFileName(st.Name))
			}
			return walkVolume(v, base, parseSnap(rec.Snap), 0, 0, func(e walkEntry) bool {
				rel := strings.TrimPrefix(e.Path, base)
				rel = strings.TrimPrefix(rel, "/")
				if rel == "" {
					return true
				}
				if e.IsDir {
					dirs = append(dirs, rel)
				} else {
					files = append(files, item{Abs: e.Path, Rel: rel})
				}
				return true
			})
		}
		// 单个文件
		files = append(files, item{Abs: srcPath, Rel: safeFileName(st.Name)})
		dstRoot = rec.Dir
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return err
	}
	for _, d := range dirs {
		_ = os.MkdirAll(filepath.Join(dstRoot, safePath(d)), 0o755)
	}
	mountMu.Lock()
	rec.Total = len(files)
	mountMu.Unlock()
	failed := 0
	for i, f := range files {
		mountMu.Lock()
		dead := rec.dead
		mountMu.Unlock()
		if dead {
			return nil
		}
		if i%8 == 0 {
			if free, ok := freeSpaceOf(rec.Dir); ok && free < 1<<30 {
				mountMu.Lock()
				rec.Err = fmt.Sprintf("本机剩余空间不足 1GB，已停止导出（已导出 %d 个文件）", rec.Files)
				mountMu.Unlock()
				return nil
			}
		}
		src := f.Abs
		dst := filepath.Join(dstRoot, safePath(f.Rel))
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := withAPFS(rec.Device, rec.Offset, 0, rec.FsOid, func(c *apfsContainer) error {
			v, err := findVolume(c, rec.FsOid)
			if err != nil {
				return err
			}
			n, rerr := copyFileTo(v, src, parseSnap(rec.Snap), rec.Device, rec.Offset, dst)
			if rerr != nil {
				return rerr
			}
			mountMu.Lock()
			rec.Files++
			rec.Bytes += n
			mountMu.Unlock()
			return nil
		}); err != nil {
			failed++
			logf("export %s: %v", src, err)
			continue
		}
	}
	if failed > 0 {
		logf("export 结束：%d 个文件失败（明细见本行之前的日志）", failed)
	}
	return nil
}

// safePath 把相对路径逐段清洗（Windows 不允许 : * ? " < > | 之类字符）
func safePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = safeFileName(parts[i])
	}
	return filepath.Join(parts...)
}
