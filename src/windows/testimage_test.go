package main

import (
	"io"
	"os"
	"testing"
)

// testImagePath 测试镜像位置：优先 APFSLENS_TEST_IMAGE，否则 /tmp/apfs.img。
func testImagePath() string {
	if p := os.Getenv("APFSLENS_TEST_IMAGE"); p != "" {
		return p
	}
	return "/tmp/apfs.img"
}

// openTestContainer 打开测试镜像，同时返回 APFS 分区在镜像里的起始偏移。
// 测试镜像既可能是裸 APFS 容器（块 0 直接是 NXSB），也可能是带 GPT 的整盘
// 镜像 —— 以前测试硬编码“块 0 就是 NXSB”，镜像一换就全红，这里两种都吃。
func openTestContainer(t *testing.T) (*os.File, *apfsContainer, int64, int64) {
	t.Helper()
	p := testImagePath()
	f, err := os.Open(p)
	if err != nil {
		t.Skipf("没有测试镜像：%v", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatalf("stat: %v", err)
	}
	size := st.Size()

	if c, err := openAPFSContainer(f, size); err == nil {
		return f, c, 0, size
	}

	parts, _, err := parseAppPartitionTable(f, 0)
	if err != nil || len(parts) == 0 {
		f.Close()
		t.Fatalf("镜像既不是裸 APFS 容器，也没有分区表：%v", err)
	}
	part := parts[0]
	for _, p := range parts {
		if p.GUID == "7C3457EF-0000-11AA-AA11-00306543ECAC" {
			part = p
			break
		}
	}
	c, err := openAPFSContainer(io.NewSectionReader(f, part.Off, part.Size), part.Size)
	if err != nil {
		f.Close()
		t.Fatalf("按分区偏移 %d 打开失败：%v", part.Off, err)
	}
	return f, c, part.Off, part.Size
}
