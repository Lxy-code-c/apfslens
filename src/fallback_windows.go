//go:build windows

package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed ui.html
var uiFS embed.FS

func uiHTMLText() string {
	b, err := uiFS.ReadFile("ui.html")
	if err != nil {
		return "<h1>ui.html 缺失</h1>"
	}
	return string(b)
}

var (
	procMB = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
)

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMB.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x00000040|0x00001000)
}

// webview2Runtime 检测机器上是否装了 WebView2 运行时（Edge/Chrome 都算）。
// 装了才去创建原生窗口；没装就直接走浏览器模式，避免去踩库里那些
// 失败即 os.Exit 的分支（那种退出连日志都留不下，表现就是「闪一下就没了」）。
func webview2Runtime() bool {
	keys := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
	}
	for _, k := range keys {
		v, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := v.GetStringValue("pv")
		v.Close()
		if err == nil && strings.TrimSpace(pv) != "" {
			return true
		}
	}
	return false
}

// 浏览器兜底：把同一套界面和 RPC 跑在本机 HTTP 上，
// 用系统默认浏览器当窗口。这样即使机器上没有 WebView2 运行时也能用。
func browserFallback(reason string) {
	logf("fallback to browser: %s", reason)

	// 本机 HTTP 端口如果没有任何校验，同机的其他程序（甚至网页）
	// 都能 POST /rpc/apiFormat 去格式化磁盘。这里加一个随机令牌：
	// 页面里注入的 shim 带 X-UDisk-Token，服务端逐个请求校验。
	tkBytes := make([]byte, 16)
	_, _ = rand.Read(tkBytes)
	token := hex.EncodeToString(tkBytes)
	lastHit := int64(time.Now().Unix())

	html := uiHTMLText()
	// 注入 shim：window.apiX(...) -> POST /rpc/apiX
	shim := `<script>
(function(){
  var names = %s;
  var TK = "%s";
  names.forEach(function(n){
    window[n] = function(){
      var args = Array.prototype.slice.call(arguments).map(function(x){ return x==null ? "" : String(x); });
      return fetch("/rpc/"+n, {method:"POST", headers:{"Content-Type":"application/json","X-UDisk-Token":TK}, body: JSON.stringify(args)})
        .then(function(r){ return r.text(); });
    };
  });
  // 页面关闭时告诉后台可以退出，避免进程一直挂着
  window.addEventListener("beforeunload", function(){
    try { navigator.sendBeacon("/bye"); } catch(e) {}
  });
})();
</script>
`
	names := make([]string, 0, 40)
	for k := range rpcTable() {
		names = append(names, k)
	}
	nj, _ := json.Marshal(names)
	// 注入点必须在 body 之前：直接插在 "const HAS_APP =" 前面会钻进
	// 已有的 <script> 块内部，把 JS 语法撑坏（旧写法就是这个错）。
	html = strings.Replace(html, "<body", string(formatShim2(shim, string(nj), token))+"<body", 1)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		logf("listen failed: %v", err)
		messageBox("启动失败", "无法监听本机端口："+err.Error())
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt64(&lastHit, time.Now().Unix())
		// 只认带令牌的地址，避免本机任意网页/程序拿到界面
		if r.URL.Query().Get("t") != token {
			http.Error(w, "403 forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = io.WriteString(w, html)
	})
	bye := make(chan struct{}, 1)
	mux.HandleFunc("/bye", func(w http.ResponseWriter, r *http.Request) {
		select {
		case bye <- struct{}{}:
		default:
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("/rpc/", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt64(&lastHit, time.Now().Unix())
		if r.Header.Get("X-UDisk-Token") != token {
			http.Error(w, "403 forbidden", http.StatusForbidden)
			return
		}
		// 拒绝跨站来源，进一步降低被网页调用破坏性接口的可能
		if o := r.Header.Get("Origin"); o != "" {
			if !strings.HasPrefix(o, "http://127.0.0.1") && !strings.HasPrefix(o, "http://localhost") {
				http.Error(w, "403 cross-origin", http.StatusForbidden)
				return
			}
		}
		name := strings.TrimPrefix(r.URL.Path, "/rpc/")
		var args []string
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(body, &args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, executeRPC(name, args))
	})
	srv := &http.Server{Handler: mux}
	goSafe("browser-ui", func() { _ = srv.Serve(ln) })

	url := "http://" + ln.Addr().String() + "/?t=" + token
	logf("open browser ui: %s", url)
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	messageBox("U盘格式化工具已在浏览器打开",
		"界面已用默认浏览器打开：\n"+url+"\n\n程序需要一直在后台为这个页面提供服务，\n所以点「确定」后它不会退出（这是正常的）。\n用完后关闭浏览器页面，再在任务栏退出本程序即可。")
	// 关键：以前这里直接 return，进程立刻退出，浏览器页面随即全部失效，
	// 表现就是「闪一下就没了」。现在主 goroutine 挂住，HTTP 服务继续跑，
	// 等页面关闭（beforeunload → /bye）再优雅退出。
	// 双保险：页面关掉（/bye）或者长时间没有任何请求（浏览器崩了/被强关）
	// 就退出，不留一个看不见的僵尸进程。
	for {
		select {
		case <-bye:
			logf("browser page closed, exiting")
			time.Sleep(500 * time.Millisecond)
			return
		case <-time.After(30 * time.Second):
			if time.Now().Unix()-atomic.LoadInt64(&lastHit) > 30*60 {
				logf("no request for 30min, exiting")
				return
			}
		}
	}
}

// formatShim2 依次替换模板里的两个 %s（接口名数组、令牌）
func formatShim2(tmpl, jsonNames, token string) []byte {
	out := make([]byte, 0, len(tmpl)+len(jsonNames)+len(token))
	vals := []string{jsonNames, token}
	vi := 0
	for i := 0; i < len(tmpl); i++ {
		if vi < len(vals) && i+1 < len(tmpl) && tmpl[i] == '%' && tmpl[i+1] == 's' {
			out = append(out, vals[vi]...)
			vi++
			i++
			continue
		}
		out = append(out, tmpl[i])
	}
	return out
}

func formatShim(tmpl, jsonNames string) []byte {
	out := make([]byte, 0, len(tmpl)+len(jsonNames))
	for i := 0; i < len(tmpl); i++ {
		if i+1 < len(tmpl) && tmpl[i] == '%' && tmpl[i+1] == 's' {
			out = append(out, jsonNames...)
			i++
			continue
		}
		out = append(out, tmpl[i])
	}
	return out
}
