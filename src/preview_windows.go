//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type previewItem struct {
	ID     string
	Name   string
	Path   string // 本地缓存文件
	Kind   string
	Mime   string
	Size   int64
	At     time.Time
	CapErr string
}

var (
	preHttpOnce sync.Once
	preBaseURL  string
	preToken    string
	preMu       sync.Mutex
	preItems    = map[string]*previewItem{}
)

func previewKind(name string) (kind, mime string) {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".heic", ".heif", ".tif", ".tiff", ".raw", ".cr2", ".nef", ".dng":
		// Edge/WebView2 自身不支持这些格式，交给「系统程序打开」或导出
		return "other", "image/" + strings.TrimPrefix(ext, ".")
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".ico":
		return "image", "image/" + strings.TrimPrefix(ext, ".")
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		return "video", "video/mp4"
	case ".mp3", ".m4a", ".aac", ".wav", ".flac", ".aiff", ".caf", ".ogg":
		return "audio", "audio/mpeg"
	case ".pdf":
		return "pdf", "application/pdf"
	case ".txt", ".md", ".log", ".csv", ".json", ".xml", ".yml", ".yaml", ".ini", ".conf", ".go", ".c", ".h", ".py", ".js", ".css", ".html", ".htm", ".sh", ".swift", ".toml", ".plist":
		return "text", "text/plain; charset=utf-8"
	}
	return "binary", "application/octet-stream"
}

func startPreviewServer() {
	preHttpOnce.Do(func() {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		preToken = hex.EncodeToString(b)

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			ln, err = net.Listen("tcp", "127.0.0.1:38217")
			if err != nil {
				return
			}
		}
		preBaseURL = "http://" + ln.Addr().String()

		mux := http.NewServeMux()
		mux.HandleFunc("/f/", func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/f/"), "/")
			if len(parts) < 2 || parts[0] != preToken {
				http.Error(w, "403", http.StatusForbidden)
				return
			}
			id := parts[1]
			preMu.Lock()
			it, ok := preItems[id]
			preMu.Unlock()
			if !ok {
				http.Error(w, "404", http.StatusNotFound)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Cache-Control", "private, max-age=3600")
			w.Header().Set("Content-Type", it.Mime)
			http.ServeFile(w, r, it.Path)
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		goSafe("preview-server", func() { _ = srv.Serve(ln) })
	})
}

func previewCacheDir() string {
	d := filepath.Join(appDataDir(), "preview")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// preparePreview 把 APFS 内的文件落到本地缓存，再交给内置 HTTP 服务。
// 这样图片 / 视频 / PDF 在 WebView 里可以直接打开，且支持拖动进度条（Range）。
func preparePreview(dev string, offset int64, oid uint64, path, snap string) (map[string]any, error) {
	startPreviewServer()
	if preBaseURL == "" {
		return nil, fmt.Errorf("预览服务启动失败")
	}
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return nil, fmt.Errorf("请选择具体文件")
	}

	base := safeFileName(strings.ReplaceAll(strings.Trim(path, "/"), "/", "_"))
	if len(base) > 80 {
		base = base[:80]
	}
	h := fnvHash(fmt.Sprintf("%s|%d|%d|%s|%s", dev, offset, oid, path, snap))
	cp := ""

	var name, kind, mime string
	var size, got int64
	err := withAPFS(dev, offset, 0, oid, func(c *apfsContainer) error {
		v, err := findVolume(c, oid)
		if err != nil {
			return err
		}
		st, err := v.stat(path, parseSnap(snap))
		if err != nil {
			return err
		}
		// 缓存键带上修改时间：只比大小会把「同大小但内容改过」的文件
		// 当成命中缓存，预览出来是旧内容。
		h = fnvHash(fmt.Sprintf("%s|%d|%d|%s|%s|%d", dev, offset, oid, path, snap, st.Mod))
		if st.Type == "dir" {
			return fmt.Errorf("这是目录，已为你打开目录列表")
		}
		name = st.Name
		kind, mime = previewKind(name)
		size = int64(st.Size)
		if fi, err := os.Stat(filepath.Join(previewCacheDir(), h+"_"+base)); err == nil && fi.Size() == int64(st.Size) && st.Size > 0 {
			cp = filepath.Join(previewCacheDir(), h+"_"+base)
			return nil // 命中缓存
		}
		cp = filepath.Join(previewCacheDir(), h+"_"+base)
		f, err := os.Create(cp + ".part")
		if err != nil {
			return err
		}
		n, rerr := v.readFile(path, parseSnap(snap), f)
		f.Close()
		if rerr != nil {
			os.Remove(cp + ".part")
			return rerr
		}
		if err := os.Rename(cp+".part", cp); err != nil {
			return err
		}
		got = n
		return nil
	})
	if err != nil {
		return nil, err
	}
	sz := size
	if sz == 0 {
		if fi, err := os.Stat(cp); err == nil {
			sz = fi.Size()
		}
	}

	id := fnvHash(fmt.Sprintf("%s%d", cp, time.Now().UnixNano()))
	preMu.Lock()
	preItems[id] = &previewItem{ID: id, Name: name, Path: cp, Kind: kind, Mime: mime, Size: sz}
	if len(preItems) > 40 {
		for k := range preItems {
			if k != id {
				delete(preItems, k)
				break
			}
		}
	}
	preMu.Unlock()

	url := fmt.Sprintf("%s/f/%s/%s/%s", preBaseURL, preToken, id, safeFileName(name))
	return map[string]any{
		"url": url, "name": name, "kind": kind, "mime": mime, "size": sz, "cached": got > 0,
		"local": cp,
	}, nil
}

func fnvHash(s string) string {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
