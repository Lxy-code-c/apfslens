package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 隐藏项判定必须覆盖所有调用方（列目录、搜索、导出）。
func TestHidden(t *testing.T) {
	for _, n := range []string{".DS_Store", ".", "..", ".Spotlight-V100", "._x", "..hidden"} {
		if !hidden(n) {
			t.Fatalf("%q 应判定为隐藏项", n)
		}
	}
	for _, n := range []string{"README.md", "a.DS_Store", "a..hidden", "normal"} {
		if hidden(n) {
			t.Fatalf("%q 不该判定为隐藏项", n)
		}
	}
}

// 根地址必须直接返回界面，而不是文件列表（这正是“还要点文件夹”的来源）。
func TestServeUI(t *testing.T) {
	cases := map[string]int{"/": 200, "/index.html": 200, "/ui.html": 200, "/go.mod": 404, "/x/y": 404}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		serveUI(rec, req)
		if rec.Code != want {
			t.Fatalf("%s 状态 = %d，期望 %d", path, rec.Code, want)
		}
		if want == 404 {
			continue
		}
		if !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("%s 返回的不是界面", path)
		}
	}
}

// 别的网站页面不能靠跨站请求读本机的盘。
func TestGuardOrigin(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	h := guard(ok)

	foreign := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/sys", nil)
	foreign.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, foreign)
	if rec.Code != 403 {
		t.Fatalf("跨站 Origin 未被拦：%d", rec.Code)
	}

	same := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/sys", nil)
	same.Header.Set("Origin", "http://127.0.0.1:1234")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, same)
	if rec.Code != 200 {
		t.Fatalf("本机 Origin 被误拦：%d", rec.Code)
	}

	none := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/sys", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, none)
	if rec.Code != 200 {
		t.Fatalf("无 Origin 的本地请求被误拦：%d", rec.Code)
	}
}

// 解析 panic 不能把进程带走。
func TestGuardRecover(t *testing.T) {
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sys", nil))
	if rec.Code != 500 {
		t.Fatalf("panic 未转成 500：%d", rec.Code)
	}
}

func TestURLEscape(t *testing.T) {
	if got := urlEscape("a b.txt"); got != "a%20b.txt" {
		t.Fatalf("urlEscape = %q", got)
	}
	if got := urlEscape("报告.md"); !strings.HasPrefix(got, "%E6%8A%A5") {
		t.Fatalf("中文未按 UTF-8 百分号编码：%q", got)
	}
}
