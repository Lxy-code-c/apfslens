package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/blacktop/go-apfs/types"
)

// 造一块「GPT + APFS 分区」的虚拟磁盘，验证真实读盘路径：
//  1. 自己解析 GPT 能拿到准确的分区偏移（不是 0、不是猜的）；
//  2. 在那个偏移上能解析出 APFS 容器、卷、目录、文件。
//
// 这正是用户那块 A 盘的场景：Windows 列不出分区，只能靠原始表。
// apfsPayload 从 go-apfs 自带的 test.dmg 里取出一段真实的 APFS 容器字节。
func apfsPayload(t *testing.T) []byte {
	t.Helper()
	src := "/home/user/gopath/pkg/mod/github.com/blacktop/go-apfs@v1.0.33/testdata/test.dmg"
	if _, err := os.Stat(src); err != nil {
		t.Skip("没有 test.dmg")
	}
	dm, err := dmg.Open(src, nil)
	if err != nil {
		t.Skip(err)
	}
	defer dm.Close()
	for i := range dm.Partitions {
		p := &dm.Partitions[i]
		buf := make([]byte, int64(p.SectorCount)*512)
		if _, err := p.ReadAt(buf, 0); err != nil && err != io.EOF {
			continue
		}
		if len(buf) > 4096 && string(buf[32:36]) == "NXSB" {
			return buf
		}
	}
	t.Fatalf("dmg 里没找到 APFS 分区")
	return nil
}

// MBR 分区表也要能找到 APFS 分区（部分 U 盘 / 转接盒就这么写）。
func TestMBROffsetAPFS(t *testing.T) {
	const sec int64 = 512
	const startLBA int64 = 2048
	data := apfsPayload(t)
	f, err := os.Create(filepath.Join(t.TempDir(), "mbr.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	total := startLBA*sec + int64(len(data)) + sec
	if err := f.Truncate(total); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(data, startLBA*sec); err != nil {
		t.Fatal(err)
	}
	mbr := make([]byte, sec)
	mbr[446+4] = 0xAF // Apple HFS/APFS（MBR 类型码）
	binary.LittleEndian.PutUint32(mbr[446+8:], uint32(startLBA))
	binary.LittleEndian.PutUint32(mbr[446+12:], uint32(int64(len(data))/sec))
	mbr[510], mbr[511] = 0x55, 0xAA
	if _, err := f.WriteAt(mbr, 0); err != nil {
		t.Fatal(err)
	}

	parts, table, err := parseAppPartitionTable(f, sec)
	if err != nil {
		t.Fatalf("MBR 解析失败：%v", err)
	}
	if table != "MBR" || len(parts) != 1 || parts[0].Off != startLBA*sec {
		t.Fatalf("MBR 结果不对：table=%q parts=%+v", table, parts)
	}
	sr := io.NewSectionReader(f, parts[0].Off, parts[0].Size)
	if _, err := openContainerLocked(sr, parts[0].Size); err != nil {
		t.Fatalf("MBR 分区内解析 APFS 失败：%v", err)
	}
	t.Logf("MBR 解析通过：offset=%d", parts[0].Off)
}

// Apple 分区图（Mac 上把盘做成 APM 时会出现）。
func TestAPMOffsetAPFS(t *testing.T) {
	const blk int64 = 512
	const partBlock int64 = 4096 // 8 个块之后放 APFS
	data := apfsPayload(t)
	f, err := os.Create(filepath.Join(t.TempDir(), "apm.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	total := partBlock*blk + int64(len(data)) + blk
	if err := f.Truncate(total); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(data, partBlock*blk); err != nil {
		t.Fatal(err)
	}

	ddm := make([]byte, blk)
	copy(ddm[0:2], "ER")
	binary.BigEndian.PutUint16(ddm[2:4], uint16(blk))
	if _, err := f.WriteAt(ddm, 0); err != nil {
		t.Fatal(err)
	}
	// 两个条目：第 1 个是分区图自己，第 2 个是 Apple_APFS
	writeAPMEntry := func(entryBlk, start, length int64, name, typ string) {
		b := make([]byte, blk)
		copy(b[0:2], "PM")
		binary.BigEndian.PutUint32(b[4:8], 2)
		binary.BigEndian.PutUint32(b[8:12], uint32(start))
		binary.BigEndian.PutUint32(b[12:16], uint32(length))
		copy(b[16:48], name)
		copy(b[48:80], typ)
		if _, err := f.WriteAt(b, entryBlk*blk); err != nil {
			t.Fatal(err)
		}
	}
	writeAPMEntry(1, 1, 7, "Apple", "Apple_partition_map")
	writeAPMEntry(2, partBlock, int64(len(data))/blk, "APFS", "Apple_APFS")

	parts, table, err := parseAppPartitionTable(f, blk)
	if err != nil {
		t.Fatalf("APM 解析失败：%v", err)
	}
	if !strings.Contains(table, "APM") || len(parts) != 2 {
		t.Fatalf("APM 结果不对：table=%q parts=%+v", table, parts)
	}
	if parts[1].Off != partBlock*blk || !strings.Contains(parts[1].Label, "APFS") {
		t.Fatalf("APM 第二个分区不对：%+v", parts[1])
	}
	sr := io.NewSectionReader(f, parts[1].Off, parts[1].Size)
	if _, err := openContainerLocked(sr, parts[1].Size); err != nil {
		t.Fatalf("APM 分区内解析 APFS 失败：%v", err)
	}
	t.Logf("APM 解析通过：offset=%d label=%s", parts[1].Off, parts[1].Label)
}

func TestGPTOffsetAPFS(t *testing.T) {
	src := "/home/user/gopath/pkg/mod/github.com/blacktop/go-apfs@v1.0.33/testdata/test.dmg"
	if _, err := os.Stat(src); err != nil {
		t.Skip("没有 test.dmg，跳过")
	}
	const sec int64 = 512
	apfsData := apfsPayload(t)
	imgSize := int64(len(apfsData))
	const partStartLBA int64 = 2048 // 1MiB 对齐，和真盘一致
	total := partStartLBA*sec + imgSize + sec

	f, err := os.Create(filepath.Join(t.TempDir(), "disk.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(total); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(apfsData, partStartLBA*sec); err != nil {
		t.Fatal(err)
	}

	// MBR 保护项
	mbr := make([]byte, sec)
	mbr[446+4] = 0xEE
	binary.LittleEndian.PutUint32(mbr[446+8:], 1)
	binary.LittleEndian.PutUint32(mbr[446+12:], uint32(total/sec-1))
	mbr[510], mbr[511] = 0x55, 0xAA
	if _, err := f.WriteAt(mbr, 0); err != nil {
		t.Fatal(err)
	}
	// GPT 头
	hdr := make([]byte, sec)
	copy(hdr[0:8], "EFI PART")
	binary.LittleEndian.PutUint64(hdr[72:], 2)
	binary.LittleEndian.PutUint32(hdr[80:], 4)
	binary.LittleEndian.PutUint32(hdr[84:], 128)
	if _, err := f.WriteAt(hdr, sec); err != nil {
		t.Fatal(err)
	}
	// 分区项 0 = APFS
	ents := make([]byte, 4*128)
	copy(ents[0:16], []byte{0xEF, 0x57, 0x34, 0x7C, 0x00, 0x00, 0xAA, 0x11, 0xAA, 0x11, 0x00, 0x30, 0x65, 0x43, 0xEC, 0xAC})
	binary.LittleEndian.PutUint64(ents[32:], uint64(partStartLBA))
	binary.LittleEndian.PutUint64(ents[40:], uint64(partStartLBA+imgSize/sec-1))
	if _, err := f.WriteAt(ents, 2*sec); err != nil {
		t.Fatal(err)
	}

	parts, table, err := parseAppPartitionTable(f, sec)
	if err != nil {
		t.Fatalf("解析分区表失败：%v", err)
	}
	if table != "GPT" {
		t.Fatalf("表类型 = %q，应为 GPT", table)
	}
	if len(parts) != 1 {
		t.Fatalf("分区数 = %d，应为 1", len(parts))
	}
	if parts[0].Off != partStartLBA*sec {
		t.Fatalf("分区偏移 = %d，应为 %d", parts[0].Off, partStartLBA*sec)
	}
	if !strings.Contains(parts[0].GUID, "7C3457EF") {
		t.Fatalf("GUID 未被识别为 APFS：%q", parts[0].GUID)
	}
	t.Logf("GPT 解析：offset=%d size=%d guid=%s", parts[0].Off, parts[0].Size, parts[0].GUID)

	// 在分区偏移处打开容器（走和真机一样的 openContainerLocked）
	types.BLOCK_SIZE = 0 // 故意清零，验证 probeBlockSize 自愈
	sr := io.NewSectionReader(f, parts[0].Off, parts[0].Size)
	c, err := openContainerLocked(sr, parts[0].Size)
	if err != nil {
		t.Fatalf("分区内解析 APFS 失败：%v", err)
	}
	if len(c.volumes) == 0 {
		t.Fatalf("没有解析出卷")
	}
	v := c.volumes[0]
	t.Logf("卷 = %q 块大小=%d 容量=%d", v.Name, c.bs, v.Capacity)
	entries, err := v.listDir("/", 0)
	if err != nil || len(entries) == 0 {
		t.Fatalf("列根目录失败：%v", err)
	}
	var buf bytes.Buffer
	if _, err := v.readFile("/README.md", 0, &buf); err != nil {
		t.Fatalf("读 README.md 失败：%v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("README.md 内容为空")
	}
	head := buf.String()
	if len(head) > 20 {
		head = head[:20]
	}
	t.Logf("分区内读文件成功：%d 字节，开头 %q", buf.Len(), head)
}
