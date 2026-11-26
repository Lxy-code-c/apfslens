// Command mk-test-image 把 macOS 的 .dmg 里的 Apple_APFS 分区抽成裸镜像，
// 供单元测试使用（单元测试需要一块 APFS 容器，不需要整个 dmg 解码链）。
//
//	go run ./tools/mk-test-image -src test.dmg -out /tmp/apfs.img
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
)

func main() {
	src := flag.String("src", "test.dmg", "输入 dmg")
	out := flag.String("out", "/tmp/apfs.img", "输出裸 APFS 镜像")
	flag.Parse()

	d, err := dmg.Open(*src, &dmg.Config{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 dmg 失败：", err)
		os.Exit(1)
	}
	defer d.Close()

	idx := -1
	for i, p := range d.Partitions {
		if strings.Contains(p.Name, "APFS") {
			idx = i
			break
		}
	}
	if idx < 0 {
		fmt.Fprintln(os.Stderr, "dmg 里没有 Apple_APFS 分区")
		os.Exit(1)
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建输出失败：", err)
		os.Exit(1)
	}
	bw := bufio.NewWriter(f)
	if err := d.Partitions[idx].Write(bw); err != nil {
		fmt.Fprintln(os.Stderr, "导出分区失败：", err)
		os.Exit(1)
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "落盘失败：", err)
		os.Exit(1)
	}
	f.Close()
	st, _ := os.Stat(*out)
	fmt.Printf("已生成 %s（%d 字节，分区 %s）\n", *out, st.Size(), d.Partitions[idx].Name)
}
