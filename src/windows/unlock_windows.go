//go:build windows

package main

import (
	"fmt"
	"io"

	"github.com/blacktop/go-apfs/types"
	fde "github.com/go-fde/apfs"
)

// 只读包装：go-fde 要求 WriteAt/Closer，但解锁只读盘不应写盘。
type roBlock struct{ d *winDevice }

func (r *roBlock) ReadAt(p []byte, off int64) (int, error) { return r.d.ReadAt(p, off) }
func (r *roBlock) WriteAt(p []byte, off int64) (int, error) {
	return 0, fmt.Errorf("只读模式，禁止写入")
}
func (r *roBlock) Close() error { return nil }

type unlockResult struct {
	Container *apfsContainer
	Dev       *fde.Device
	Reader    io.ReaderAt
}

// unlockContainer 用 FileVault 密码解密整个容器（纯 Go，无需驱动）。
func unlockContainer(dev string, offset, size int64, pass string, oidHint uint64) (*unlockResult, error) {
	d, err := openWinDevice(dev, offset, size)
	if err != nil {
		return nil, fmt.Errorf("打开设备失败：%v", err)
	}
	rd := &roBlock{d: d}
	dec, err := fde.OpenFrom(rd, []byte(pass))
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("密码不正确或容器没有可用的 FileVault 密钥：%v", err)
	}
	// 真实 FileVault 容器里，块 0 的 NXSB 和 checkpoint 描述区是明文，
	// 只有卷元数据块是密文。所以顺序应该是：
	//   1) 块 0 走明文、其余走解密（最常见）
	//   2) 全部走解密
	//   3) 块 0 + checkpoint 描述区走明文
	var c *apfsContainer
	var cErr error
	sp := &splicedReader{raw: d, dec: dec, plainBlocks: 1}
	c, err = openAPFSContainer(sp, size)
	if err != nil {
		cErr = err
		c, err = openAPFSContainer(dec, size)
		if err != nil {
			cErr = fmt.Errorf("%v / 全解密直读：%v", cErr, err)
			sp2 := &splicedReader{raw: d, dec: dec, plainBlocks: 1}
			if nxRaw, e := types.ReadObj(d, 0); e == nil {
				base := reflectUint(nxRaw.Body, "XpDescBase")
				cnt := reflectUint(nxRaw.Body, "XpDescBlocks") &^ (1 << 31)
				if base > 0 && cnt > 0 && cnt < 64 {
					sp2.plainFrom, sp2.plainCount = int64(base), int64(cnt)
				}
			}
			c, err = openAPFSContainer(sp2, size)
			if err != nil {
				dec.Close()
				d.Close()
				return nil, fmt.Errorf("解密后仍无法解析 APFS 元数据：%v", cErr)
			}
		}
	}
	// 校验：至少一个卷能列出根目录
	okVol := false
	var lastErr error
	for _, v := range c.volumes {
		if v.fsRoot == nil {
			continue
		}
		if _, err := v.listDir("/", 0); err == nil {
			okVol = true
			break
		} else {
			lastErr = err
		}
	}
	if !okVol {
		dec.Close()
		d.Close()
		if lastErr == nil {
			lastErr = fmt.Errorf("没有可读卷")
		}
		return nil, fmt.Errorf("解锁失败：%v", lastErr)
	}
	return &unlockResult{Container: c, Dev: dec, Reader: dec}, nil
}

// installUnlocked 把解密后的容器换进缓存，之后所有读取都走解密视图。
func installUnlocked(dev string, offset int64, res *unlockResult) {
	key := apfsKey(dev, offset)
	apfsCacheMu.Lock()
	defer apfsCacheMu.Unlock()
	if h, ok := apfsCache[key]; ok {
		h.c = res.Container
		h.dec = res.Dev
		h.unlocked = true
		h.unlockPass = ""
		h.last = nowUnix()
	} else {
		apfsCache[key] = &apfsHandle{c: res.Container, key: key, dec: res.Dev, unlocked: true, last: nowUnix()}
	}
}

// splicedReader：块 0（和 checkpoint 描述区）走明文，其余走解密视图。
type splicedReader struct {
	raw         io.ReaderAt
	dec         io.ReaderAt
	plainBlocks int64
	plainFrom   int64
	plainCount  int64
}

func (s *splicedReader) ReadAt(p []byte, off int64) (int, error) {
	bs := int64(types.BLOCK_SIZE)
	if bs <= 0 {
		bs = 4096
	}
	blk := off / bs
	if blk < s.plainBlocks || (s.plainCount > 0 && blk >= s.plainFrom && blk < s.plainFrom+s.plainCount) {
		return s.raw.ReadAt(p, off)
	}
	return s.dec.ReadAt(p, off)
}
