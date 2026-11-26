package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 用 Linux 版 C++ 模块验证 job 文件格式与搬运结果（Windows 侧同一份源码）。
func TestNativeJobFormat(t *testing.T) {
	bin := "/tmp/native_linux"
	if _, err := os.Stat(bin); err != nil {
		t.Skip("linux native build absent")
	}
	src := "/tmp/apfs.img"
	if _, err := os.Stat(src); err != nil {
		t.Skip("no raw image")
	}
	dst := t.TempDir() + "/out.bin"
	job := t.TempDir() + "/job.txt"
	exts := []byteExtent{{Off: 4096 * 2, Len: 4096, Dst: 0}, {Off: 4096 * 9, Len: 2048, Dst: 4096}}
	var b strings.Builder
	fmt.Fprintf(&b, "src=%s\ndst=%s\ntotal=6144\n", src, dst)
	for _, e := range exts {
		fmt.Fprintf(&b, "ext %d %d %d\n", e.Off, e.Len, e.Dst)
	}
	if err := os.WriteFile(job, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "copy", job).CombinedOutput()
	if err != nil {
		t.Fatalf("native: %v %s", err, out)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("bad output %s", out)
	}
	got, _ := os.ReadFile(dst)
	want := make([]byte, 6144)
	raw, _ := os.ReadFile(src)
	copy(want[0:4096], raw[4096*2:4096*3])
	copy(want[4096:6144], raw[4096*9:4096*9+2048])
	if string(got) != string(want) {
		t.Fatalf("内容不一致：got %d bytes", len(got))
	}
	t.Logf("native copy ok, %d bytes", len(got))
}
