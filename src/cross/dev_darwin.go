//go:build darwin

package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const osTag = "darwin"

const (
	dkiocGetBlockSize  = 0x40046418 // DKIOCGETBLOCKSIZE  _IOR('d', 24, uint32)
	dkiocGetBlockCount = 0x40086419 // DKIOCGETBLOCKCOUNT _IOR('d', 25, uint64)
)

func deviceSize(f *os.File, hint int64) int64 {
	var bs uint32
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(dkiocGetBlockSize), uintptr(unsafe.Pointer(&bs))); e != 0 || bs == 0 {
		return hint
	}
	var n uint64
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(dkiocGetBlockCount), uintptr(unsafe.Pointer(&n))); e != 0 || n == 0 {
		return hint
	}
	return int64(n) * int64(bs)
}

func isBlockDevice(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFBLK || st.Mode&syscall.S_IFMT == syscall.S_IFCHR
}
