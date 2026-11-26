package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// 自己解析分区表（GPT / MBR / Apple Partition Map），不依赖 Get-Partition。
// 专业读取工具（libfsapfs、apfs-fuse）都是这么干的：Windows 对某些磁盘
// （尤其是 Mac 上拆下来的、被识别成 RAW 的盘）根本列不出分区，
// 只有直接从 LBA0/LBA1 读原始表才拿得到准确偏移。

type rawPart struct {
	Off   int64  // 分区起始字节偏移
	Size  int64  // 分区大小
	GUID  string // GPT 类型 GUID（大写、无花括号）；MBR/APM 为空
	Label string // 分区名
	Table string // GPT / MBR / APM
}

func readFull(r io.ReaderAt, buf []byte, off int64) (int, error) {
	n, err := r.ReadAt(buf, off)
	if n < len(buf) && err == nil {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// guidString 按 GUID 规范解码：前 8 字节小端（4+2+2），后 8 字节大端。
// 直接按字节顺序输出会得到「EF57347C-0000-AA11-…」这种错位字符串，
// 拿它去和 APFS 的 7C3457EF-0000-11AA-AA11-00306543ECAC 永远匹配不上。
func guidString(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	return strings.ToUpper(fmt.Sprintf("%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		binary.LittleEndian.Uint32(b[0:4]),
		binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]),
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15]))
}

// parseAppPartitionTable 是平台无关版本：传任意可随机读对象即可。
// Windows 侧由 parsePartitionTable 用物理盘句柄调用；单测直接喂文件。
func parseAppPartitionTable(r io.ReaderAt, sec int64) ([]rawPart, string, error) {
	if sec < 512 {
		sec = 512
	}

	blk0 := make([]byte, 4096)

	// ---- Apple Partition Map：DDM 块 0 的签名是 "ER" ----
	if n, e := readFull(r, blk0, 0); e == nil && n >= 512 && string(blk0[0:2]) == "ER" {
		apmBlock := int64(binary.BigEndian.Uint16(blk0[2:4]))
		if apmBlock < 512 || apmBlock > 4096 {
			apmBlock = 512
		}
		if parts, ok := parseAPM(r, apmBlock); ok {
			return parts, "APM(Apple 分区图)", nil
		}
	}

	// ---- GPT：LBA1 是头，签名 "EFI PART" ----
	hdr := make([]byte, sec)
	if n, e := readFull(r, hdr, sec); e == nil && n >= 92 && string(hdr[0:8]) == "EFI PART" {
		entriesLBA := int64(binary.LittleEndian.Uint64(hdr[72:80]))
		numEntries := int64(binary.LittleEndian.Uint32(hdr[80:84]))
		entrySize := int64(binary.LittleEndian.Uint32(hdr[84:88]))
		if entriesLBA > 0 && numEntries > 0 && numEntries <= 512 && entrySize >= 128 && entrySize <= 4096 {
			need := numEntries * entrySize
			if need > 1<<20 {
				need = 1 << 20
			}
			ents := make([]byte, need)
			if n2, e2 := readFull(r, ents, entriesLBA*sec); e2 == nil && int64(n2) >= entrySize {
				var out []rawPart
				for i := int64(0); i < numEntries; i++ {
					o := i * entrySize
					if o+entrySize > int64(n2) {
						break
					}
					typeGUID := guidString(ents[o : o+16])
					if typeGUID == "" || typeGUID == "00000000-0000-0000-0000-000000000000" {
						continue
					}
					first := int64(binary.LittleEndian.Uint64(ents[o+32 : o+40]))
					last := int64(binary.LittleEndian.Uint64(ents[o+40 : o+48]))
					if first <= 0 || last < first {
						continue
					}
					// 分区名：偏移 56 起 72 字节 UTF-16LE（最多 36 个字符）
					var labelU16 [36]uint16
					for k := 0; k < 36; k++ {
						p := o + 56 + int64(k)*2
						labelU16[k] = binary.LittleEndian.Uint16(ents[p : p+2])
					}
					label := strings.TrimSpace(string(utf16.Decode(labelU16[:])))
					out = append(out, rawPart{
						Off: first * sec, Size: (last - first + 1) * sec,
						GUID: typeGUID, Label: label, Table: "GPT",
					})
				}
				if len(out) > 0 {
					return out, "GPT", nil
				}
			}
		}
	}

	// ---- MBR：0x55AA 在 510，四个主分区项从 446 起，每项 16 字节 ----
	if n, e := readFull(r, blk0, 0); e == nil && n >= 512 && blk0[510] == 0x55 && blk0[511] == 0xAA {
		var out []rawPart
		for i := 0; i < 4; i++ {
			o := 446 + i*16
			typ := blk0[o+4]
			start := int64(binary.LittleEndian.Uint32(blk0[o+8 : o+12]))
			count := int64(binary.LittleEndian.Uint32(blk0[o+12 : o+16]))
			if typ == 0 || start == 0 || count <= 0 {
				continue
			}
			// 0xEE 是 GPT 保护项，不是真分区
			if typ == 0xEE {
				continue
			}
			out = append(out, rawPart{Off: start * sec, Size: count * sec, Table: "MBR"})
		}
		if len(out) > 0 {
			return out, "MBR", nil
		}
	}
	return nil, "", fmt.Errorf("没有读到可识别的分区表（GPT / MBR / APM 都不匹配）")
}

// parseAPM 解析 Apple Partition Map：块 1 起每块描述一个分区。
// 字段：签名 "PM"(0)、map 项数(4)、本分区起始块(8)、块数(12)、
// 分区名(16..47)、分区类型(48..79)。
func parseAPM(r io.ReaderAt, blockSize int64) ([]rawPart, bool) {
	var out []rawPart
	var declared int64
	for blk := int64(1); blk <= 128; blk++ {
		buf := make([]byte, blockSize)
		n, err := readFull(r, buf, blk*blockSize)
		if err != nil || int64(n) < 80 {
			break
		}
		if string(buf[0:2]) != "PM" {
			break
		}
		if declared == 0 {
			declared = int64(binary.BigEndian.Uint32(buf[4:8]))
		}
		start := int64(binary.BigEndian.Uint32(buf[8:12]))
		length := int64(binary.BigEndian.Uint32(buf[12:16]))
		name := strings.TrimSpace(strings.Trim(string(buf[16:48]), "\x00"))
		typ := strings.TrimSpace(strings.Trim(string(buf[48:80]), "\x00"))
		if typ == "" {
			typ = name
		}
		if length > 0 {
			out = append(out, rawPart{Off: start * blockSize, Size: length * blockSize, Label: typ, Table: "APM"})
		}
		if declared > 0 && blk >= declared {
			break
		}
	}
	return out, len(out) > 0
}
