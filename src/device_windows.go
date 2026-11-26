//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ioctlDiskGetDriveGeometry = 0x00070000
const ioctlDiskGetLengthInfo = 0x0007405c

type diskGeometry struct {
	Cylinders         int64
	MediaType         uint32
	TracksPerCylinder uint32
	SectorsPerTrack   uint32
	BytesPerSector    uint32
}

// querySector 取物理扇区大小。4Kn 盘（原生 4096 字节扇区）上，
// 任何不是 4096 对齐的原始读都会返回 ERROR_INVALID_PARAMETER(87)，
// 这正是「盘能看到、APFS 却解析不出来」的常见原因。
func querySector(h windows.Handle) int64 {
	var g diskGeometry
	var ret uint32
	if err := windows.DeviceIoControl(h, ioctlDiskGetDriveGeometry, nil, 0,
		(*byte)(unsafe.Pointer(&g)), uint32(unsafe.Sizeof(g)), &ret, nil); err != nil {
		return 512
	}
	if g.BytesPerSector >= 512 && g.BytesPerSector <= 65536 {
		return int64(g.BytesPerSector)
	}
	return 512
}

// queryLength 取设备真实长度。物理盘上用 GetFileInformationByHandle 拿到的是 0，
// 以前就退化成「假设 1TB」，读盘尾部会一直报设备错误而不是干净的 EOF。
func queryLength(h windows.Handle) int64 {
	var n int64
	var ret uint32
	if err := windows.DeviceIoControl(h, ioctlDiskGetLengthInfo, nil, 0,
		(*byte)(unsafe.Pointer(&n)), uint32(unsafe.Sizeof(n)), &ret, nil); err != nil {
		return 0
	}
	if n > 0 {
		return n
	}
	return 0
}

// 原始物理磁盘 / 分区设备：按字节偏移随机读，内部做 4K 块缓存。
// 约定：readRaw / writeRaw 一律使用「相对本设备基址的偏移」，
// 基址（分区起始）在这里统一加上，调用方不要再自己加，避免同一处
// 逻辑在两个地方各自加一半而导致写错位置。
type winDevice struct {
	path   string
	h      windows.Handle
	base   int64 // 分区在物理磁盘上的起始偏移
	size   int64
	closed bool
	sector int64
	mu     sync.Mutex

	cache    map[int64]*cacheBlk
	cacheLRU []int64
	cacheMax int
}

type cacheBlk struct {
	data []byte
	used int64
}

func openWinDevice(diskPath string, base, size int64) (*winDevice, error) {
	p, err := windows.UTF16PtrFromString(diskPath)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED,
		0)
	if err != nil {
		return nil, fmt.Errorf("打开 %s 失败：%v%s", diskPath, err, hintForErr(err))
	}
	if size <= 0 {
		size = queryLength(h)
	}
	if size <= 0 {
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err == nil {
			size = int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
		}
	}
	if size <= 0 {
		size = 1 << 40
	}
	d := &winDevice{path: diskPath, h: h, base: base, size: size, sector: querySector(h), cache: map[int64]*cacheBlk{}, cacheMax: 512}
	return d, nil
}

// hintForErr 把常见 Win32 错误翻译成人能看懂的提示。
func hintForErr(err error) string {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "（权限不足：请用管理员身份运行，或点「解除拦截并启动.cmd」）"
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return "（磁盘被其他程序独占：关掉磁盘管理/分区软件后重试）"
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return "（参数无效：多半是扇区对齐问题，请把诊断文件发我）"
	case errors.Is(err, windows.ERROR_NOT_READY):
		return "（设备未就绪：换 USB 口或重新插拔）"
	}
	return ""
}

func (d *winDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	for k := range d.cache {
		delete(d.cache, k)
	}
	d.cacheLRU = nil
	return windows.CloseHandle(d.h)
}

func (d *winDevice) GetSize() uint64 { return uint64(d.size) }

// readRaw 读绝对物理偏移 abs（由调用方决定，通常是 d.base+相对偏移）。
// 非扇区对齐的请求会自动扩读到扇区边界再裁回来。
func (d *winDevice) readRaw(buf []byte, abs int64) (int, error) {
	if abs < 0 {
		abs = 0
	}
	sec := d.sector
	if sec < 512 {
		sec = 512
	}
	if abs%sec != 0 || int64(len(buf))%sec != 0 {
		return d.readRawAligned(buf, abs, sec)
	}
	d.mu.Lock()
	h := d.h
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return 0, errors.New("设备已关闭")
	}
	ov := &windows.Overlapped{Offset: uint32(abs & 0xffffffff), OffsetHigh: uint32(abs >> 32)}
	var done uint32
	err := windows.ReadFile(h, buf, &done, ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(h, ov, &done, true)
	}
	if err != nil && err != windows.ERROR_HANDLE_EOF && err != windows.ERROR_MORE_DATA {
		return int(done), err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

// ReadAt 读 [off, off+len)，off 相对本设备基址，自动做 512 字节对齐。
func (d *winDevice) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, errors.New("负偏移")
	}
	if d.size > 0 && off >= d.size {
		return 0, io.EOF
	}
	end := off + int64(len(p))
	if d.size > 0 && end > d.size {
		clipped := len(p) - int(end-d.size)
		if clipped <= 0 {
			return 0, io.EOF
		}
		p = p[:clipped]
		end = off + int64(len(p))
	}
	// 整块 4K 走缓存（APFS 绝大多数读都是 4K 对齐）
	if off%4096 == 0 && len(p)%4096 == 0 && len(p) <= 64*1024 {
		n := 0
		for n < len(p) {
			blk, err := d.cachedBlock(off + int64(n))
			if err != nil {
				return n, err
			}
			if len(blk) == 0 {
				return n, io.EOF
			}
			c := copy(p[n:], blk)
			if c == 0 {
				break
			}
			n += c
		}
		return n, nil
	}
	// 非对齐：按 512 字节扇区扩读
	const align = int64(512)
	start := off - off%align
	sz := end - start
	sz = (sz + align - 1) / align * align
	tmp := make([]byte, sz)
	n, err := d.readRaw(tmp, d.base+start)
	if int64(n) <= off-start {
		if err == nil || err == io.EOF {
			err = io.EOF
		}
		return 0, err
	}
	copied := copy(p, tmp[off-start:int64(n)])
	if copied < len(p) && err == nil {
		err = io.EOF
	}
	return copied, err
}

func (d *winDevice) readRawAligned(buf []byte, abs, sec int64) (int, error) {
	start := abs - abs%sec
	end := abs + int64(len(buf))
	endAligned := (end + sec - 1) / sec * sec
	span := endAligned - start
	tmp := make([]byte, span)
	n, err := d.readRawDirect(tmp, start)
	if n == 0 {
		return 0, err
	}
	if int64(n) <= abs-start {
		return 0, io.EOF
	}
	return copy(buf, tmp[abs-start:int64(n)]), nil
}

// readRawDirect 只接受扇区对齐的读（真正的 ReadFile 在这里）。
func (d *winDevice) readRawDirect(buf []byte, abs int64) (int, error) {
	d.mu.Lock()
	h := d.h
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return 0, errors.New("设备已关闭")
	}
	ov := &windows.Overlapped{Offset: uint32(abs & 0xffffffff), OffsetHigh: uint32(abs >> 32)}
	var done uint32
	err := windows.ReadFile(h, buf, &done, ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(h, ov, &done, true)
	}
	if err != nil && err != windows.ERROR_HANDLE_EOF && err != windows.ERROR_MORE_DATA {
		return int(done), err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

func (d *winDevice) cachedBlock(off int64) ([]byte, error) {
	d.mu.Lock()
	if b, ok := d.cache[off]; ok {
		b.used++
		data := b.data
		d.mu.Unlock()
		return data, nil
	}
	d.mu.Unlock()

	buf := make([]byte, 4096)
	n, err := d.readRaw(buf, d.base+off)
	if err != nil && n == 0 {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, err
	}
	if n < 4096 {
		buf = buf[:n]
	}
	d.mu.Lock()
	if _, dup := d.cache[off]; dup {
		d.mu.Unlock()
		return buf, nil
	}
	if len(d.cache) >= d.cacheMax && len(d.cacheLRU) > 0 {
		old := d.cacheLRU[0]
		d.cacheLRU = d.cacheLRU[1:]
		delete(d.cache, old)
	}
	d.cache[off] = &cacheBlk{data: buf}
	d.cacheLRU = append(d.cacheLRU, off)
	d.mu.Unlock()
	return buf, nil
}

func (d *winDevice) ReadFileTo(w io.Writer, off, length int64) error {
	sr := io.NewSectionReader(d, off, length)
	_, err := io.Copy(w, sr)
	return err
}

func openWinDeviceRW(diskPath string, base, size int64) (*winDevice, error) {
	p, err := windows.UTF16PtrFromString(diskPath)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED,
		0)
	if err != nil {
		return nil, fmt.Errorf("打开 %s（读写）失败：%v%s", diskPath, err, hintForErr(err))
	}
	if size <= 0 {
		size = queryLength(h)
	}
	if size <= 0 {
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err == nil {
			size = int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
		}
	}
	return &winDevice{path: diskPath, h: h, base: base, size: size, sector: querySector(h), cache: map[int64]*cacheBlk{}, cacheMax: 8}, nil
}

// writeRawAbs 写绝对物理偏移；off 必须是 512 字节对齐、buf 长度也应对齐。
func (d *winDevice) writeRawAbs(buf []byte, abs int64) (int, error) {
	d.mu.Lock()
	h := d.h
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return 0, errors.New("设备已关闭")
	}
	ov := &windows.Overlapped{Offset: uint32(abs & 0xffffffff), OffsetHigh: uint32(abs >> 32)}
	var done uint32
	err := windows.WriteFile(h, buf, &done, ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(h, ov, &done, true)
	}
	if err != nil {
		return int(done), err
	}
	return int(done), nil
}

// writeRaw 写相对本设备基址的偏移（旧版漏加 base，会把模板写到分区外，
// 直接盖掉目标盘的 MBR/GPT —— 这是数据安全问题，已修复）。
func (d *winDevice) writeRaw(buf []byte, off int64) (int, error) {
	return d.writeRawAligned(buf, off)
}

// writeRawAligned 做「读-改-写」的扇区对齐写：先把边界补成 512 的整数倍，
// 避免非对齐写被 Windows 直接拒绝（物理盘写入要求扇区对齐）。
func (d *winDevice) writeRawAligned(buf []byte, off int64) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	const align = int64(512)
	start := off - off%align
	end := off + int64(len(buf))
	endAligned := (end + align - 1) / align * align
	span := endAligned - start

	tmp := make([]byte, span)
	// 先用原内容填充（读失败也继续，全 0 兜底）
	if n, err := d.readRaw(tmp, d.base+start); err != nil && n == 0 && err != io.EOF {
		return 0, fmt.Errorf("对齐读失败：%v", err)
	}
	copy(tmp[off-start:], buf)
	if _, err := d.writeRawAbs(tmp, d.base+start); err != nil {
		return 0, fmt.Errorf("写入失败：%v", err)
	}
	return len(buf), nil
}

// WriteAt 满足 io.WriterAt，供 rawWriter 之类的顺序写使用。
func (d *winDevice) WriteAt(p []byte, off int64) (int, error) {
	return d.writeRawAligned(p, off)
}
