package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/blacktop/go-apfs/types"
)

func TestAPFS(t *testing.T) {
	dm, err := dmg.Open("/home/user/gopath/pkg/mod/github.com/blacktop/go-apfs@v1.0.33/testdata/test.dmg", nil)
	if err != nil {
		t.Skip(err)
	}
	defer dm.Close()
	var parsed int
	for i := range dm.Partitions {
		p := &dm.Partitions[i]
		c, err := openAPFSContainer(p, int64(p.SectorCount)*512)
		if err != nil {
			t.Logf("partition %d: %v", i, err)
			continue
		}
		parsed++
		fmt.Println("bs", c.bs, "volumes", len(c.volumes))
		for _, v := range c.volumes {
			fmt.Printf(" vol %q role=%s cap=%d used=%d files=%d dirs=%d snaps=%d sealed=%v\n", v.Name, v.Role, v.Capacity, v.Used, v.Files, v.Dirs, v.Snaps, v.Sealed)
			ents, err := v.listDir("/", 0)
			fmt.Println("  entries err:", err)
			for _, e := range ents {
				fmt.Printf("  - %s type=%s size=%d mtime=%d\n", e.Name, e.Type, e.Size, e.Mtime)
			}
			for _, e := range ents {
				if e.Type == "file" {
					var buf []byte
					w := &sliceWriter{&buf}
					n, err := v.readFile("/"+e.Name, 0, w)
					fmt.Printf("  read %s n=%d err=%v head=%q\n", e.Name, n, err, string(buf[:min(len(buf), 40)]))
				}
			}
			if snaps, err := v.snapshots(); err == nil {
				fmt.Println("  snapshots:", len(snaps))
				for _, s := range snaps {
					fmt.Printf("   - %s xid=%d\n", s.Name, s.Xid)
				}
			}
		}
	}
	if parsed == 0 {
		t.Fatalf("没有任何分区解析成 APFS 容器")
	}
}

type sliceWriter struct{ b *[]byte }

func (s *sliceWriter) Write(p []byte) (int, error) { *s.b = append(*s.b, p...); return len(p), nil }

func min(a, b int) int { if a < b { return a }; return b }

// 稀疏/碎片文件的读出内容必须逐字节正确：空洞补零、顺序按逻辑偏移。
func TestCopyExtentsSparse(t *testing.T) {
	bs := uint64(4096)
	// 两个数据块：块 1 全 'A'，块 2 全 'B'
	img := make([]byte, bs*4)
	for i := 0; i < 4096; i++ {
		img[int(bs)+i] = 'A'
		img[int(2*bs)+i] = 'B'
	}
	var fexts []types.FileExtent
	fexts = append(fexts, types.FileExtent{
		Address: 0, Block: 1, Length: 100,
	})
	fexts = append(fexts, types.FileExtent{
		Address: 200, Block: 2, Length: 50,
	})
	var buf bytes.Buffer
	n, err := copyExtents(&buf, bytes.NewReader(img), bs, 250, fexts)
	if err != nil {
		t.Fatalf("copyExtents: %v", err)
	}
	if n != 250 {
		t.Fatalf("长度 = %d, 期望 250", n)
	}
	out := buf.Bytes()
	if string(out[:100]) != strings.Repeat("A", 100) {
		t.Fatalf("前 100 字节不对：%q", out[:10])
	}
	for i := 100; i < 200; i++ {
		if out[i] != 0 {
			t.Fatalf("空洞第 %d 字节 = %d, 期望 0", i, out[i])
		}
	}
	if string(out[200:]) != strings.Repeat("B", 50) {
		t.Fatalf("尾部 50 字节不对：%q", out[200:210])
	}
}

// 段长度超过文件大小时必须截断，不能写出多余数据。
func TestCopyExtentsTruncate(t *testing.T) {
	bs := uint64(4096)
	img := make([]byte, bs*2)
	for i := 0; i < 4096; i++ {
		img[int(bs)+i] = 'X'
	}
	fexts := []types.FileExtent{{Address: 0, Block: 1, Length: 4096}}
	var buf bytes.Buffer
	n, err := copyExtents(&buf, bytes.NewReader(img), bs, 10, fexts)
	if err != nil || n != 10 || buf.Len() != 10 {
		t.Fatalf("n=%d err=%v len=%d, 期望 10", n, err, buf.Len())
	}
	if buf.String() != strings.Repeat("X", 10) {
		t.Fatalf("内容不对：%q", buf.String())
	}
}
