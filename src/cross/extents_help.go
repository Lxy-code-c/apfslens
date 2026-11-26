package main

import (
	"fmt"
	"os"
)

// byteExtent 是「相对分区起点」的一段连续字节，供 C++ 原生搬运模块使用。
type byteExtent struct {
	Off int64 // 源（分区内）偏移
	Len int64 // 长度
	Dst int64 // 目标文件内的逻辑偏移（稀疏空洞因此保持为 0）
}

// nativeCopyHook 由平台文件（native_copy_windows.go）注入；为 nil 时走 Go 自己的读路径。
var nativeCopyHook func(dev string, partOff int64, exts []byteExtent, dst string, total int64) error

// copyFileTo 把卷内文件写到本机路径 dst。
// 大文件（>8MB）且有 C++ 原生模块时，交给多线程搬运；否则回退 Go 的解析器读。
func copyFileTo(v *apfsVolume, path string, snap uint64, dev string, partOff int64, dst string) (int64, error) {
	if nativeCopyHook != nil && !v.Encrypted {
		// 只对「明文、未压缩、数据段总和 == 文件大小」的普通文件走原生搬运：
		// 加密卷的数据段是密文、压缩文件（.dmg/.png 等 decmpfs）的数据段不是
		// 最终内容，交给原生模块会拷出乱码，必须走 Go 的解压/解密路径。
		st, serr := v.stat(path, snap)
		if serr == nil && st.Type == "file" {
			if exts, total, err := v.dataExtents(path, snap); err == nil && total > 8<<20 && total == int64(st.Size) {
				tp := dst + ".udisk-part"
				nerr := nativeCopyHook(dev, partOff, exts, tp, total)
				if nerr == nil {
					if fi, serr := os.Stat(tp); serr == nil && fi.Size() == total {
						if rerr := os.Rename(tp, dst); rerr == nil {
							return total, nil
						}
					}
					_ = os.Remove(tp)
				} else {
					_ = os.Remove(tp)
					logf("native copy fallback: %v", nerr)
				}
			}
		}
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, rerr := v.readFile(path, snap, f)
	cerr := f.Close()
	if rerr != nil {
		_ = os.Remove(dst)
		return 0, rerr
	}
	if cerr != nil {
		return n, cerr
	}
	return n, nil
}

// dataExtents 取某个文件的物理数据段（相对分区起点），并按逻辑顺序拼好。
// 稀疏空洞不在这里产生，只返回真实有数据的段；调用方按顺序写入目标文件。
func (v *apfsVolume) dataExtents(path string, snap uint64) ([]byteExtent, int64, error) {
	omap, root, err := v.view(snap)
	if err != nil {
		return nil, 0, err
	}
	oid, _, err := v.lookup(path, snap)
	if err != nil {
		return nil, 0, err
	}
	fexts, err := v.extentsOf(omap, root, oid, snap)
	if err != nil {
		return nil, 0, err
	}
	if len(fexts) == 0 {
		return nil, 0, fmt.Errorf("该文件没有数据段")
	}
	st, serr := v.stat(path, snap)
	if serr != nil {
		return nil, 0, serr
	}
	size := int64(st.Size)
	out := make([]byteExtent, 0, len(fexts))
	var total int64
	for _, fe := range fexts {
		l := int64(fe.Length)
		if l <= 0 {
			continue
		}
		dst := int64(fe.Address) * int64(v.c.bs)
		if dst >= size {
			continue
		}
		if dst+l > size {
			l = size - dst // 最后一段只拷到文件真实结尾，避免多拷整块
		}
		out = append(out, byteExtent{Off: int64(fe.Block) * int64(v.c.bs), Len: l, Dst: dst})
		total += l
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("该文件的数据段长度都是 0")
	}
	return out, total, nil
}
