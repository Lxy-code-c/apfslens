//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const osTag = "linux"

// BLKGETSIZE64：返回块设备的字节长度。
func deviceSize(f *os.File, hint int64) int64 {
	var out uint64
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(0x80081272), uintptr(unsafe.Pointer(&out)))
	if errno == 0 && out > 0 {
		return int64(out)
	}
	return hint
}

func isBlockDevice(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFBLK
}
