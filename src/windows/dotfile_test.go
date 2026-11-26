package main

import (
	"strings"
	"testing"
)

// 隐藏项（. 开头）不应出现在列表里。
func TestDotfilesHidden(t *testing.T) {
	f, c, _, _ := openTestContainer(t)
	defer f.Close()
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
