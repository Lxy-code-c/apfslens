package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

)

// 容器缓存：同一 (磁盘, 偏移) 只打开一次，避免每次点目录都重新扫一遍。
type openVol struct {
	key  string
	file *os.File
	c    *apfsContainer
	used time.Time
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*openVol{}
)

const maxOpen = 4

func openTarget(path string, off int64, size int64) (*apfsContainer, error) {
	key := fmt.Sprintf("%s@%d", path, off)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if o, ok := cache[key]; ok {
		o.used = time.Now()
		return o.c, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		size = deviceSize(f, size)
	}
	if size <= 0 {
		f.Close()
		return nil, errors.New("无法获取大小，请确认路径是可访问的磁盘或镜像文件")
	}
	c, err := openAPFSContainer(io.NewSectionReader(f, off, size), size)
	if err != nil {
		f.Close()
		return nil, err
	}
	if len(cache) >= maxOpen {
		var oldest *openVol
		for _, v := range cache {
			if oldest == nil || v.used.Before(oldest.used) {
				oldest = v
			}
		}
		if oldest != nil {
			oldest.file.Close()
			delete(cache, oldest.key)
		}
	}
	cache[key] = &openVol{key: key, file: f, c: c, used: time.Now()}
	return c, nil
}

func jwrite(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func jok(w http.ResponseWriter, data any) {
	jwrite(w, map[string]any{"ok": true, "data": data})
}

func jerr(w http.ResponseWriter, err error) {
	jwrite(w, map[string]any{"ok": false, "error": err.Error()})
}

func qInt(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

// 隐藏项（. 开头）一律不返回：列表、搜索、导出全走这里。
func hidden(name string) bool { return strings.HasPrefix(name, ".") }

func handleScan(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		jerr(w, errors.New("请填写磁盘路径"))
		return
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	f, err := os.Open(path)
	if err != nil {
		jerr(w, err)
		return
	}
	defer f.Close()
	size := qInt(r, "size")
	st, err := f.Stat()
	if err == nil && st.Mode().IsRegular() {
		size = st.Size()
	} else {
		size = deviceSize(f, size)
	}
	parts, table, err := parseAppPartitionTable(f, 0)
	if err != nil || len(parts) == 0 {
		// 没分区表：多半直接把分区/镜像丢进来了，按整块当容器试一次。
		c, cerr := openAPFSContainer(io.NewSectionReader(f, 0, size), size)
		if cerr == nil {
			jok(w, map[string]any{
				"path": path, "size": size, "table": "无（按整块 APFS 处理）",
				"parts": []map[string]any{{"off": 0, "size": size, "label": filepath.Base(path), "apfs": true,
					"volumes": len(c.volumes)}},
			})
			return
		}
		if err != nil {
			jerr(w, err)
		} else {
			jerr(w, fmt.Errorf("没有找到分区表，也不像 APFS 容器：%v", cerr))
		}
		return
	}
	out := make([]map[string]any, 0, len(parts))
	for i, p := range parts {
		item := map[string]any{"index": i, "off": p.Off, "size": p.Size, "label": p.Label,
			"guid": p.GUID, "apfs": p.GUID == "7C3457EF-0000-11AA-AA11-00306543ECAC"}
		if item["apfs"].(bool) {
			if c, err := openAPFSContainer(io.NewSectionReader(f, p.Off, p.Size), p.Size); err == nil {
				names := make([]string, 0, len(c.volumes))
				for _, v := range c.volumes {
					names = append(names, v.Name)
				}
				item["volumes"] = names
			} else {
				item["error"] = err.Error()
			}
		}
		out = append(out, item)
	}
	jok(w, map[string]any{"path": path, "size": size, "table": table, "parts": out})
}

func handleVolumes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	c, err := openTarget(path, qInt(r, "off"), qInt(r, "size"))
	if err != nil {
		jerr(w, err)
		return
	}
	list := make([]map[string]any, 0, len(c.volumes))
	for _, v := range c.volumes {
		list = append(list, map[string]any{
			"oid": uint64(v.oid), "name": v.Name, "role": roleName(uint16(v.sb.Role)),
			"sealed": v.Sealed, "encrypted": v.Encrypted, "usable": v.Usable,
		})
	}
	jok(w, map[string]any{"volumes": list, "notes": c.notes})
}

func findVol(c *apfsContainer, oid uint64) (*apfsVolume, error) {
	for _, v := range c.volumes {
		if uint64(v.oid) == oid {
			return v, nil
		}
	}
	return nil, fmt.Errorf("卷不存在：%d", oid)
}

func handleList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	c, err := openTarget(path, qInt(r, "off"), qInt(r, "size"))
	if err != nil {
		jerr(w, err)
		return
	}
	v, err := findVol(c, uint64(qInt(r, "oid")))
	if err != nil {
		jerr(w, err)
		return
	}
	p := r.URL.Query().Get("p")
	if p == "" {
		p = "/"
	}
	entries, err := v.listDir(p, uint64(qInt(r, "snap")))
	if err != nil {
		jerr(w, err)
		return
	}
	list := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if hidden(e.Name) {
			continue
		}
		list = append(list, map[string]any{"name": e.Name, "type": e.Type, "size": e.Size,
			"mtime": e.Mtime, "is_dir": e.Type == "dir", "oid": uint64(e.Oid)})
	}
	sort.Slice(list, func(i, j int) bool {
		di, _ := list[i]["is_dir"].(bool)
		dj, _ := list[j]["is_dir"].(bool)
		if di != dj {
			return di
		}
		return strings.ToLower(list[i]["name"].(string)) < strings.ToLower(list[j]["name"].(string))
	})
	jok(w, map[string]any{"path": p, "entries": list, "name": v.Name})
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	c, err := openTarget(path, qInt(r, "off"), qInt(r, "size"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	v, err := findVol(c, uint64(qInt(r, "oid")))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	p := r.URL.Query().Get("p")
	if strings.Contains(p, "/.") || strings.HasPrefix(p, ".") {
		http.Error(w, "隐藏项不提供导出", 403)
		return
	}
	st, err := v.stat(p, uint64(qInt(r, "snap")))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	name := filepath.Base(p)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(st.Size), 10))
	if _, err := v.readFile(p, uint64(qInt(r, "snap")), w); err != nil {
		// 头已发出，只能记录
		println("download error:", err.Error())
	}
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// 卷内搜索：递归遍历，跳过隐藏项，最多返回 200 条。
func handleSearch(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	kw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if kw == "" {
		jerr(w, errors.New("请输入搜索关键词"))
		return
	}
	c, err := openTarget(path, qInt(r, "off"), qInt(r, "size"))
	if err != nil {
		jerr(w, err)
		return
	}
	v, err := findVol(c, uint64(qInt(r, "oid")))
	if err != nil {
		jerr(w, err)
		return
	}
	hits := make([]map[string]any, 0, 200)
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 8 || len(hits) >= 200 {
			return
		}
		entries, err := v.listDir(dir, 0)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(hits) >= 200 {
				return
			}
			if hidden(e.Name) {
				continue
			}
			full := strings.TrimSuffix(dir, "/") + "/" + e.Name
			if strings.Contains(strings.ToLower(e.Name), kw) {
				hits = append(hits, map[string]any{"name": e.Name, "path": full,
					"is_dir": e.Type == "dir", "size": e.Size})
			}
			if e.Type == "dir" {
				walk(full, depth+1)
			}
		}
	}
	walk("/", 0)
	jok(w, map[string]any{"hits": hits, "count": len(hits)})
}

func handleSnaps(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	c, err := openTarget(path, qInt(r, "off"), qInt(r, "size"))
	if err != nil {
		jerr(w, err)
		return
	}
	v, err := findVol(c, uint64(qInt(r, "oid")))
	if err != nil {
		jerr(w, err)
		return
	}
	snaps, _ := v.snapshots()
	list := make([]map[string]any, 0, len(snaps))
	for _, s := range snaps {
		list = append(list, map[string]any{"xid": s.Xid, "name": s.Name, "time": s.Created})
	}
	jok(w, list)
}

func handleSys(w http.ResponseWriter, r *http.Request) {
	jok(w, map[string]any{"os": runtimeGOOS(), "arch": runtimeGOARCH(), "version": appVersion})
}
