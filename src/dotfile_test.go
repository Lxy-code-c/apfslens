package main

import (
	"os"
	"strings"
	"testing"
)

// 隐藏项（. 开头）不应出现在列表里。
func TestDotfilesHidden(t *testing.T) {
	f, err := os.Open("/tmp/apfs.img")
	if err != nil {
		t.Skip("no raw image")
	}
	defer f.Close()
	st, _ := f.Stat()
	c, err := openAPFSContainer(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.volumes {
		ents, err := v.listDir("/", 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name, ".") {
				t.Fatalf("隐藏项泄漏：%q", e.Name)
			}
		}
		if len(ents) == 0 {
			t.Fatalf("根目录为空")
		}
	}
}
