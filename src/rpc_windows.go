//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/blacktop/go-apfs/types"
	"golang.org/x/sys/windows"
)

const appVersion = "1.0.0"

func jok(v any) string {
	b, err := json.Marshal(map[string]any{"ok": true, "data": v})
	if err != nil {
		return jerr(err)
	}
	return string(b)
}

func jerr(e any) string {
	var msg string
	switch x := e.(type) {
	case error:
		msg = x.Error()
	case string:
		msg = x
	default:
		msg = fmt.Sprintf("%v", x)
	}
	b, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
	return string(b)
}

func pInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func pUint(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

// ---------------- 磁盘概览 ----------------

func apiScan() string {
	defer logCall("apiScan")()
	disks, err := scanDisks()
	if err != nil {
		return jerr(err)
	}
	apfsCount := 0
	for i := range disks {
		if disks[i].Apfs {
			apfsCount++
		}
	}
	return jok(map[string]any{
		"platform": "windows", "version": appVersion, "admin": isAdmin(),
		"apfsCount": apfsCount, "disks": disks,
	})
}

func apiApfsDevices() string {
	disks, err := scanDisks()
	if err != nil {
		return jerr(err)
	}
	return jok(disks)
}

func apiSupport() string { return jok(map[string]any{"fs": fsSupport()}) }

// ---------------- APFS 卷 / 目录 ----------------

func apiApfsVolumes(dev string) string {
	defer logCall("apiApfsVolumes")()
	vols, diag, err := scanAPFSVolumes(dev)
	if diag == nil {
		diag = map[string]string{}
	}
	// 失败也把原因写进诊断里：用户不用回传 log 就能在界面上看到卡在哪一步。
	if err != nil {
		diag["错误"] = err.Error()
	}
	if len(diag) > 0 {
		var sb strings.Builder
		sb.WriteString("apfslens diag " + time.Now().Format("2006-01-02 15:04:05") + "\n")
		sb.WriteString("device: " + dev + "\n")
		for k, v := range diag {
			sb.WriteString(fmt.Sprintf("%s = %s\n", k, v))
		}
		_ = os.MkdirAll(downloadsDir(), 0o755)
		_ = os.WriteFile(filepath.Join(downloadsDir(), "apfslens-diag.txt"), []byte(sb.String()), 0o644)
	}
	if err != nil && len(vols) == 0 {
		return jok(map[string]any{"volumes": []apfsVolView{}, "diag": diag, "error": err.Error()})
	}
	return jok(map[string]any{"volumes": vols, "diag": diag})
}

func apiApfsList(dev string, offset, oid uint64, path, snap string) string {
	defer logCall("apiApfsList")()
	var out map[string]any
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		if path == "" {
			path = "/"
		}
		entries, err := v.listDir(path, parseSnap(snap))
		if err != nil {
			return err
		}
		list := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			// 出口兜底：无论上游哪条路径漏了过滤，界面都不会收到隐藏项。
			if strings.HasPrefix(e.Name, ".") {
				continue
			}
			list = append(list, map[string]any{
				"name": e.Name, "type": e.Type, "size": e.Size, "mtime": e.Mtime, "is_dir": e.Type == "dir",
			})
		}
		out = map[string]any{"name": v.Name, "path": path, "entries": list}
		return nil
	})
	if err != nil {
		return jerr(err)
	}
	return jok(out)
}

func apiApfsInfo(dev string, offset, oid uint64) string {
	defer logCall("apiApfsInfo")()
	var out map[string]any
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		fields := volumeViewFields(v)
		fields["role"] = v.Role
		if h := cachedHandle(dev, int64(offset)); h != nil && h.unlocked {
			fields["encrypted"] = false
			fields["unlocked"] = true
		}
		out = fields
		return nil
	})
	if err != nil {
		return jerr(err)
	}
	return jok(out)
}

func cachedHandle(dev string, offset int64) *apfsHandle {
	apfsCacheMu.Lock()
	defer apfsCacheMu.Unlock()
	if h, ok := apfsCache[apfsKey(dev, offset)]; ok {
		return h
	}
	return nil
}

func apiApfsSnapshots(dev string, offset, oid uint64) string {
	defer logCall("apiApfsSnapshots")()
	var snaps []map[string]any
	var note string
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		list, err := v.snapshots()
		if err != nil {
			note = err.Error()
			return nil
		}
		for _, s := range list {
			snaps = append(snaps, map[string]any{
				"name": s.Name, "xid": s.Xid, "sblock_oid": s.Sblock, "created": s.Created,
			})
		}
		if len(snaps) == 0 {
			note = "该卷没有 Time Machine / 本地快照"
		}
		return nil
	})
	if err != nil {
		return jerr(err)
	}
	if snaps == nil {
		snaps = []map[string]any{}
	}
	return jok(map[string]any{"snapshots": snaps, "note": note})
}

func apiApfsUnlock(dev string, offset, oid uint64, pass string) string {
	defer logCall("apiApfsUnlock")()
	res, err := unlockContainer(dev, int64(offset), 0, pass, oid)
	if err != nil {
		return jerr(err)
	}
	installUnlocked(dev, int64(offset), res)
	// 不要关掉原来的明文设备句柄：解锁后的拼接视图（块 0/checkpoint 区走明文）
	// 还在用它，关了就变成「解锁成功但一读就失败」。
	return jok(map[string]any{"unlocked": true})
}

// 解锁后把旧的（未解密）设备句柄关掉，避免句柄泄漏；解密视图保留。
func dropLockedOnly(dev string, offset int64) {
	key := apfsKey(dev, offset)
	apfsCacheMu.Lock()
	defer apfsCacheMu.Unlock()
	if h, ok := apfsCache[key]; ok && h.dev != nil {
		h.dev.Close()
		h.dev = nil
	}
}

// ---------------- 文件操作 ----------------

func apiApfsDownload(dev string, offset, oid uint64, path, snap string) string {
	defer logCall("apiApfsDownload")()
	var dst string
	var size int64
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		st, err := v.stat(path, parseSnap(snap))
		if err != nil {
			return err
		}
		if st.Type == "dir" {
			return fmt.Errorf("目录请用「导出此目录」")
		}
		_ = os.MkdirAll(downloadsDir(), 0o755)
		nm := safeFileName(st.Name)
		dst = filepath.Join(downloadsDir(), nm)
		ext := filepath.Ext(nm)
		base := strings.TrimSuffix(nm, ext)
		uniq := true
		for i := 1; i <= 500; i++ {
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				uniq = true
				break
			}
			uniq = false
			// base/ext 都必须是清洗过的名字，否则 ":" "/" 这类字符拼出来
			// 的路径在 Windows 上会失败；超过 500 次也不能覆盖同名文件。
			dst = filepath.Join(downloadsDir(), fmt.Sprintf("%s (%d)%s", base, i, ext))
		}
		if !uniq {
			dst = filepath.Join(downloadsDir(), fmt.Sprintf("%s (%d)%s", base, time.Now().Unix(), ext))
		}
		n, err := copyFileTo(v, path, parseSnap(snap), dev, int64(offset), dst)
		size = n
		return err
	})
	if err != nil {
		return jerr(err)
	}
	// 导出后给一个 SHA-256（大文件最多算到 2GB，避免为了校验把几十 GB 再读一遍）：
	// 用户拿它和 macOS 上的 `shasum -a 256` 对比，就能确认导出完整无损。
	sum := ""
	if size > 0 && size <= 2<<30 {
		if h, herr := fileSHA256(dst); herr == nil {
			sum = h
		}
	}
	return jok(map[string]any{"path": dst, "size": size, "sha256": sum})
}

func apiApfsExport(dev string, offset, oid uint64, path, snap string) string {
	defer logCall("apiApfsExport")()
	t := newTask("export", dev, "apfs", path, "export")
	id := newMountID()
	dir := mountDir(id)
	_ = os.MkdirAll(dir, 0o755)
	rec := &mountRec{
		ID: id, Device: dev, Offset: int64(offset), FsOid: oid, Snap: snap,
		Dir: dir, At: time.Now().Unix(), State: "syncing", RO: true, Auto: false,
	}
	mountMu.Lock()
	mountRecs[id] = rec
	mountMu.Unlock()

	goSafe("export", func() {
		// 关键：先把清单收集完（一次短锁），再在锁外逐个复制。
		// 以前整个导出在 withAPFS 闭包里跑，等于全程占着 APFS 全局锁，
		// 导出期间界面点任何东西都会像卡死。
		err := exportPath(rec, path)
		mountMu.Lock()
		if err != nil {
			rec.State = "error"
			rec.Err = err.Error()
		} else {
			rec.State = "ready"
		}
		files := rec.Files
		nbytes := rec.Bytes
		mountMu.Unlock()
		savePrefs()
		t.stage("完成", 1)
		if err != nil {
			t.finish(false, err.Error())
		} else {
			t.finish(true, fmt.Sprintf("已导出 %d 个文件 · %d 字节", files, nbytes))
		}
	})
	return jok(map[string]any{"task": t.ID})
}

func apiApfsFileInfo(dev string, offset, oid uint64, path, snap string) string {
	var out map[string]any
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		st, err := v.stat(path, parseSnap(snap))
		if err != nil {
			return err
		}
		out = map[string]any{
			"name": st.Name, "type": st.Type, "size": st.Size, "uncompressed_size": st.RawSize,
			"create_time": st.Create, "mod_time": st.Mod, "change_time": st.Change, "access_time": st.Access,
			"owner": st.Owner, "group": st.Group, "mode": fmt.Sprintf("%o", st.Mode), "nlink": st.Nlink,
			"bsd_flags": st.BsdFlags, "flag": st.Flag, "protection_class": st.Protect,
			"parent_id": st.Parent, "xfields": st.Xfields,
		}
		return nil
	})
	if err != nil {
		return jerr(err)
	}
	return jok(out)
}

func apiApfsSearch(dev string, offset, oid uint64, kw, snap string) string {
	kw = strings.ToLower(strings.TrimSpace(kw))
	res := []map[string]any{}
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		return walkVolume(v, "/", parseSnap(snap), 200000, 32, func(e walkEntry) bool {
			if strings.Contains(strings.ToLower(filepath.Base(e.Path)), kw) {
				res = append(res, map[string]any{
					"path": e.Path, "size": e.Size, "is_dir": e.IsDir, "mtime": e.Mtime,
				})
			}
			return len(res) < 400
		})
	})
	if err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"results": res})
}

func apiApfsBigFiles(dev string, offset, oid uint64, snap string) string {
	files := []walkEntry{}
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		return walkVolume(v, "/", parseSnap(snap), 300000, 32, func(e walkEntry) bool {
			if !e.IsDir && e.Size > 0 {
				files = append(files, e)
			}
			return true
		})
	})
	if err != nil {
		return jerr(err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })
	if len(files) > 40 {
		files = files[:40]
	}
	out := []map[string]any{}
	for _, f := range files {
		out = append(out, map[string]any{"path": f.Path, "size": f.Size, "mtime": f.Mtime, "is_dir": false})
	}
	return jok(map[string]any{"files": out})
}

func apiApfsDiff(dev string, offset, oid uint64, axid, bxid, path string) string {
	if path == "" {
		path = "/"
	}
	collect := func(snap string) (map[string]int64, error) {
		m := map[string]int64{}
		err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
			v, err := findVolume(c, oid)
			if err != nil {
				return err
			}
			return walkVolume(v, path, parseSnap(snap), 300000, 32, func(e walkEntry) bool {
				if !e.IsDir {
					m[e.Path] = int64(e.Size)
				}
				return true
			})
		})
		return m, err
	}
	a, err := collect(axid)
	if err != nil {
		return jerr(err)
	}
	b, err := collect(bxid)
	if err != nil {
		return jerr(err)
	}
	added, removed, changed := []map[string]any{}, []map[string]any{}, []map[string]any{}
	for p, sz := range b {
		if old, ok := a[p]; !ok {
			added = append(added, map[string]any{"path": p, "size": sz})
		} else if old != sz {
			changed = append(changed, map[string]any{"path": p, "size": sz, "old_size": old})
		}
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			removed = append(removed, map[string]any{"path": p})
		}
	}
	return jok(map[string]any{
		"a_total": len(a), "b_total": len(b), "added": added, "removed": removed, "changed": changed,
	})
}

// ---------------- 预览（新增） ----------------

func apiApfsPreview(dev string, offset, oid uint64, path, snap string) string {
	defer logCall("apiApfsPreview")()
	d, err := preparePreview(dev, int64(offset), oid, path, snap)
	if err != nil {
		return jerr(err)
	}
	return jok(d)
}

// ---------------- 挂载（新增） ----------------

func apiApfsMount(dev string, offset, oid uint64, letter, snap string) string {
	defer logCall("apiApfsMount")()
	rec, err := mountVolume(dev, int64(offset), oid, letter, snap)
	if err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"id": rec.ID, "target": rec.Letter + ":", "dir": rec.Dir, "state": rec.State})
}

func apiApfsMountList() string {
	defer logCall("apiApfsMountList")()
	recs := listMounts()
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		// 盘符自愈：映射可能因为会话/权限变化被系统丢掉（用户反馈「挂上了但实际
		// 打不开」）。每次刷新列表都确认盘符真的存在，不存在就重新映射一次，
		// 并把结果如实告诉界面（visible=false 时界面要提示，不能假装成功）。
		visible := false
		if r.Letter != "" {
			if !driveVisible(r.Letter) {
				repairErr := mapDrive(r.Letter, r.Dir)
				visible = driveVisible(r.Letter)
				if !visible {
					if repairErr != nil {
						r.Err = "盘符重新映射失败：" + repairErr.Error()
					} else {
						r.Err = "盘符 " + r.Letter + ": 未能生效（可能被杀毒/组策略拦截）"
					}
				}
			} else {
				visible = true
			}
		}
		m := map[string]any{
			"id": r.ID, "volume": r.Volume, "device": r.Device, "dir": r.Dir, "files": r.Files,
			"bytes": r.Bytes, "at": r.At, "state": r.State, "letter": r.Letter, "auto": r.Auto,
			"total": r.Total, "error": r.Err, "visible": visible,
		}
		if r.Letter != "" {
			m["target"] = r.Letter + ":"
		}
		out = append(out, m)
	}
	return jok(out)
}

func apiApfsMountDrop(id string) string {
	if err := unmountVolume(id); err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"id": id})
}

func apiApfsMountSet(id, letter, auto, rebuild string) string {
	var autoPtr *bool
	if auto != "" {
		b := auto == "1" || auto == "true"
		autoPtr = &b
	}
	rec, err := mountSet(id, letter, autoPtr, rebuild == "1" || rebuild == "true")
	if err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"id": rec.ID, "letter": rec.Letter, "auto": rec.Auto, "state": rec.State})
}

// ---------------- 任务 ----------------

func apiTask(id string) string {
	t := getTask(id)
	if t == nil {
		return jerr("任务不存在")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return jok(map[string]any{
		"id": t.ID, "device": t.Device, "fs": t.Fs, "label": t.Label, "mode": t.Mode,
		"status": t.Status, "stage": t.Stage, "progress": t.Progress, "log": t.Logs,
	})
}

// ---------------- 格式化 / 分区 ----------------

func apiFormat(dev, fs, mode, label string) string {
	defer logCall("apiFormat")()
	t, err := startFormat(dev, fs, mode, label)
	if err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"task": t.ID})
}

func apiApfsQuick(dev, sizeMB, image, name, confirm string) string {
	t, err := apfsQuickFormat(dev, sizeMB, image, name, confirm)
	if err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"task": t.ID})
}

func apiApfsTemplateInfo(path string) string {
	info, err := apfsTemplateInfo(strings.Trim(path, "\""))
	if err != nil {
		return jerr(err)
	}
	return jok(info)
}

func apiApfsApplyTemplate(dev, image, offsetMB, confirm string) string {
	t, err := applyAPFSTemplate(dev, pInt(offsetMB), strings.Trim(image, "\""), confirm)
	if err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"task": t.ID})
}

func apiPartTable(dev string) string {
	defer logCall("apiPartTable")()
	m, err := partTableOf(dev)
	if err != nil {
		return jerr(err)
	}
	return jok(m)
}

func apiPartTag(dev, part, kind, confirm string) string {
	m, err := partSetTag(dev, part, kind, confirm)
	if err != nil {
		return jerr(err)
	}
	return jok(m)
}

func apiPartExec(dev, actions, confirm string) string {
	m, err := partExecPlan(dev, actions, confirm)
	if err != nil {
		return jerr(err)
	}
	return jok(m)
}

func apiPartFormatOne(dev, part, fs, label, mode, confirm string) string {
	m, err := partFormatOne(dev, part, fs, label, mode, confirm)
	if err != nil {
		return jerr(err)
	}
	return jok(m)
}

// ---------------- 磁盘工具 ----------------

func apiDuMedia(dev string) string {
	defer logCall("apiDuMedia")()
	type psDisk struct {
		Model    string `json:"Model"`
		Serial   string `json:"SerialNumber"`
		Firmware string `json:"FirmwareVersion"`
		Size     int64  `json:"Size"`
		Bus      string `json:"BusType"`
		Media    string `json:"MediaType"`
		Health   string `json:"HealthStatus"`
		OpStatus string `json:"OperationalStatus"`
	}
	var d psDisk
	script := fmt.Sprintf(`Get-PhysicalDisk | Where-Object { $_.DeviceId -eq '%d' } | Select-Object Model,SerialNumber,FirmwareVersion,Size,BusType,MediaType,HealthStatus,OperationalStatus | ConvertTo-Json -Compress`, diskNumberOf(dev))
	_ = runPSJson(script, &d)
	if d.Model == "" {
		// 退回到 Get-Disk
		var gd struct {
			Model        string `json:"Model"`
			SerialNumber string `json:"SerialNumber"`
			Firmware     string `json:"FirmwareVersion"`
			Size         int64  `json:"Size"`
			BusType      string `json:"BusType"`
			HealthStatus string `json:"HealthStatus"`
		}
		_ = runPSJson(fmt.Sprintf(`Get-Disk -Number %d | Select-Object Model,SerialNumber,FirmwareVersion,Size,BusType,HealthStatus | ConvertTo-Json -Compress`, diskNumberOf(dev)), &gd)
		d.Model, d.Serial, d.Firmware, d.Size, d.Bus, d.Health = gd.Model, gd.SerialNumber, gd.Firmware, gd.Size, gd.BusType, gd.HealthStatus
	}
	style := ""
	if m, err := partTableOf(dev); err == nil {
		if s, ok := m["style"].(string); ok {
			style = s
		}
	}
	wear := int64(-1)
	temps := int64(-1)
	var smart struct {
		Temperature int64 `json:"Temperature"`
		Wear        int64 `json:"Wear"`
		ReadErrors  int64 `json:"ReadErrors"`
		WriteErrors int64 `json:"WriteErrors"`
		PowerOn     int64 `json:"PowerOnHours"`
	}
	_ = runPSJson(fmt.Sprintf(`$d=Get-PhysicalDisk | Where-Object { $_.DeviceId -eq '%d' }; $r=@{}; try{ $r.Temperature=[int]$d.Temperature }catch{}; try{ $r.Wear=[int]$d.Wear }catch{}; try{ $r.PowerOn=[int]$d.PowerOnHours }catch{}; $r | ConvertTo-Json -Compress`, diskNumberOf(dev)), &smart)
	wear, temps = smart.Wear, smart.Temperature
	return jok(map[string]any{
		"model": d.Model, "serial": d.Serial, "firmware": d.Firmware, "size": d.Size,
		"bus": d.Bus, "media": d.Media, "partition_style": style, "op_status": d.OpStatus,
		"power_on": smart.PowerOn, "temp": temps, "wear": wear,
		"read_err": smart.ReadErrors, "write_err": smart.WriteErrors, "health": d.Health,
	})
}

func apiDuTree(dev string) string {
	defer logCall("apiDuTree")()
	var container map[string]any
	groups := []map[string]any{}
	vols := []map[string]any{}
	var off int64 = 0
	disks, err := scanDisks()
	if err != nil {
		return jerr(err)
	}
	var d *diskInfo
	for i := range disks {
		if strings.EqualFold(disks[i].Path, dev) {
			d = &disks[i]
		}
	}
	if d == nil {
		return jerr("设备不存在")
	}
	parts := apfsPartitions(*d)
	if len(parts) > 0 {
		off = parts[0].Off
	}
	err = withAPFS(dev, off, 0, 0, func(c *apfsContainer) error {
		kbP := reflectUint(c.nx, "Keylocker")
		kbB := reflectUint(c.nx, "KeybagBlkCount")
		maxFS := reflectUint(c.nx, "MaxFileSystems")
		container = map[string]any{
			"uuid": uuidString(c.nx.UUID), "block_size": c.bs, "offset": off,
			"keybag_paddr": kbP, "keybag_blocks": kbB, "max_file_systems": maxFS,
		}
		groupIdx := map[string]int{}
		for _, v := range c.volumes {
			gid := v.GroupID
			if gid == "" {
				gid = "未分组"
			}
			idx, ok := groupIdx[gid]
			if !ok {
				groups = append(groups, map[string]any{"group": gid, "volumes": []map[string]any{}})
				idx = len(groups) - 1
				groupIdx[gid] = idx
			}
			item := map[string]any{
				"name": v.Name, "role": v.Role, "volume_uuid": v.UUID, "offset": off, "fs_oid": uint64(v.oid),
			}
			groups[idx]["volumes"] = append(groups[idx]["volumes"].([]map[string]any), item)
			vols = append(vols, item)
		}
		for _, lv := range c.locked {
			item := map[string]any{"name": lv.Name, "role": lv.Role, "volume_uuid": lv.UUID, "offset": off, "fs_oid": uint64(lv.Oid), "encrypted": true}
			vols = append(vols, item)
		}
		return nil
	})
	if err != nil {
		return jerr(err)
	}
	if container == nil {
		container = map[string]any{}
	}
	return jok(map[string]any{"container": container, "groups": groups, "volumes": vols})
}

func apiDuVerify(dev, offset, image string) string {
	checks := []map[string]any{}
	pass := func(name, detail string) { checks = append(checks, map[string]any{"name": name, "status": "ok", "detail": detail}) }
	warn := func(name, detail string) { checks = append(checks, map[string]any{"name": name, "status": "warn", "detail": detail}) }
	fail := func(name, detail string) { checks = append(checks, map[string]any{"name": name, "status": "fail", "detail": detail}) }

	off := pInt(offset)
	if image != "" {
		f, err := os.Open(strings.Trim(image, "\""))
		if err != nil {
			fail("模板镜像", err.Error())
		} else {
			st, _ := f.Stat()
			c, err := openAPFSContainer(f, st.Size())
			f.Close()
			if err != nil {
				fail("模板镜像解析", err.Error())
			} else {
				pass("模板镜像", fmt.Sprintf("APFS 容器，%d 卷，块 %d", len(c.volumes), c.bs))
			}
		}
	} else {
		err := withAPFS(dev, off, 0, 0, func(c *apfsContainer) error {
			pass("容器超级块", fmt.Sprintf("块大小 %d，容器块数 %d", c.bs, c.blockCnt))
			if c.bs != 4096 {
				warn("块大小", fmt.Sprintf("%d（非 4096，部分工具可能不兼容）", c.bs))
			} else {
				pass("块大小", "4096 字节，标准值")
			}
			if len(c.volumes) == 0 {
				warn("卷", "容器内没有可读卷")
			} else {
				pass("卷", fmt.Sprintf("发现 %d 个卷：%s", len(c.volumes), volNames(c)))
			}
			for _, v := range c.volumes {
				if v.fsRoot == nil {
					warn("卷 "+v.Name, "读不到根目录树")
					continue
				}
				entries, err := v.listDir("/", 0)
				if err != nil {
					fail("卷 "+v.Name+" 根目录", err.Error())
					continue
				}
				pass("卷 "+v.Name+" 根目录", fmt.Sprintf("%d 个顶层项目", len(entries)))
				if v.Used > v.Capacity && v.Capacity > 0 {
					warn("卷 "+v.Name+" 容量", "已用大于容量，元数据可能不一致")
				}
			}
			if len(c.locked) > 0 {
				_, keybagP := reflectUint2(c.nx, "Keylocker")
				_ = keybagP
				warn("加密卷", fmt.Sprintf("%d 个 FileVault 卷需要密码", len(c.locked)))
			}
			return nil
		})
		if err != nil {
			fail("设备解析", err.Error())
		}
	}
	verdict := "ok"
	for _, c := range checks {
		if c["status"] == "fail" {
			verdict = "fail"
		} else if c["status"] == "warn" && verdict == "ok" {
			verdict = "warn"
		}
	}
	return jok(map[string]any{"checks": checks, "verdict": verdict})
}

func reflectUint2(v any, name string) (uint64, int) {
	return reflectUint(v, name), 0
}

func volNames(c *apfsContainer) string {
	var names []string
	for _, v := range c.volumes {
		names = append(names, v.Name)
	}
	return strings.Join(names, " / ")
}

func apiDuSpace(dev string, offset, oid uint64, snap string) string {
	dirSizes := map[string]int64{}
	kindSizes := map[string]int64{}
	var total int64
	err := withAPFS(dev, int64(offset), 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		return walkVolume(v, "/", parseSnap(snap), 400000, 40, func(e walkEntry) bool {
			if e.IsDir {
				return true
			}
			total += int64(e.Size)
			top := strings.Split(strings.TrimPrefix(e.Path, "/"), "/")[0]
			if top == "" {
				top = filepath.Base(e.Path)
			}
			dirSizes[top] += int64(e.Size)
			ext := strings.ToLower(filepath.Ext(e.Path))
			if ext == "" {
				ext = "无扩展名"
			}
			kindSizes[ext] += int64(e.Size)
			return true
		})
	})
	if err != nil {
		return jerr(err)
	}
	dirs := []map[string]any{}
	for k, v := range dirSizes {
		dirs = append(dirs, map[string]any{"name": k, "size": v})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["size"].(int64) > dirs[j]["size"].(int64) })
	if len(dirs) > 12 {
		dirs = dirs[:12]
	}
	kinds := []map[string]any{}
	for k, v := range kindSizes {
		kinds = append(kinds, map[string]any{"name": k, "size": v})
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i]["size"].(int64) > kinds[j]["size"].(int64) })
	if len(kinds) > 12 {
		kinds = kinds[:12]
	}
	return jok(map[string]any{"total": total, "dirs": dirs, "kinds": kinds})
}

// ---------------- Windows 卷挂载 ----------------

func apiMounts() string {
	var rows []struct {
		Letter  string `json:"Letter"`
		Label   string `json:"Label"`
		FS      string `json:"FS"`
		Size    int64  `json:"Size"`
		Free    int64  `json:"Free"`
		Type    string `json:"Type"`
		Device  string `json:"Device"`
		IsRO    bool   `json:"IsRO"`
		System  bool   `json:"System"`
	}
	_ = runPSJson(`$r=@(Get-Volume | Where-Object { $_.DriveLetter } | ForEach-Object { [pscustomobject]@{ Letter=[string]$_.Letter; Label=$_.FileSystemLabel; FS=$_.FileSystem; Size=$_.Size; Free=$_.SizeRemaining; Type=[string]$_.DriveType; Device=$_.Path; IsRO=[bool]$_.IsReadOnly; System=($_.DriveType -eq 'Fixed') } }); ConvertTo-Json -InputObject $r -Compress`, &rows)
	out := []map[string]any{}
	for _, r := range rows {
		if r.Letter == "" {
			continue
		}
		out = append(out, map[string]any{
			"target": r.Letter + ":", "fstype": r.FS, "ro": r.IsRO,
			"usage": map[string]any{"size": r.Size, "used": r.Size - r.Free, "avail": r.Free},
			"device": r.Device, "can_unmount": !r.System, "label": r.Label, "type": r.Type,
		})
	}
	return jok(out)
}

func apiMount(dev string) string {
	if !isAdmin() {
		return jerr("需要管理员权限")
	}
	letter := pickLetter()
	if letter == "" {
		return jerr("没有空闲盘符")
	}
	num := diskNumberOf(dev)
	script := fmt.Sprintf(`$p=Get-Disk -Number %d | Get-Partition | Where-Object { $_.Type -ne 'Reserved' } | Select-Object -First 1; if(-not $p){ throw '没有可用分区' }; Set-Partition -DiskNumber %d -PartitionNumber $p.PartitionNumber -NewDriveLetter %s; Write-Output "$($p.PartitionNumber)|%s"`, num, num, letter, letter)
	out, err := runPS(script)
	if err != nil {
		return jerr(fmt.Sprintf("分配盘符失败：%v %s", err, out))
	}
	return jok(map[string]any{"target": letter + ":"})
}

func apiUnmount(target string) string {
	if !isAdmin() {
		return jerr("需要管理员权限")
	}
	t := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(target), ":"))
	if len(t) != 1 {
		return jerr("盘符格式：E:")
	}
	if t == "C" || t == "A" || t == "B" {
		return jerr("C:/A:/B: 不允许卸载")
	}
	out, err := runPS(fmt.Sprintf(`mountvol %s: /p`, t))
	if err != nil {
		// 退回到 Remove-PartitionAccessPath
		if o2, e2 := runPS(fmt.Sprintf(`$v=Get-Volume -DriveLetter %s; if($v){ Remove-PartitionAccessPath -DiskNumber $v.DiskNumber -PartitionNumber $v.PartitionNumber -AccessPath "%s:\" }`, t, t)); e2 != nil {
			return jerr(fmt.Sprintf("卸载失败：%v %s %s", err, out, o2))
		}
	}
	return jok(map[string]any{"target": t + ":"})
}
func apiOpen(path string) string {
	path = strings.Trim(path, "\"")
	if path == "" {
		return jerr("路径为空")
	}
	if err := shellOpen(path, ""); err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"path": path})
}

func apiElevate() string {
	exe, err := os.Executable()
	if err != nil {
		return jerr(err)
	}
	if err := shellOpen(exe, "runas"); err != nil {
		return jerr(err)
	}
	return jok(map[string]any{"elevated": true})
}

func shellOpen(path, verb string) error {
	var verbPtr *uint16
	if verb != "" {
		verbPtr, _ = windows.UTF16PtrFromString(verb)
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	verbPtr2 := verbPtr
	return windows.ShellExecute(0, verbPtr2, file, nil, nil, windows.SW_SHOWNORMAL)
}

var _ = exec.Command
var _ = syscall.SysProcAttr{}
var _ = unsafe.Pointer(nil)
var _ = types.BLOCK_SIZE

// fileSHA256 计算文件摘要，用于导出后校验完整性。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
