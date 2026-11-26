// Package lzfse 是 github.com/blacktop/lzfse-cgo 的纯 Go 替身，
// 让 go-apfs 能在没有 cgo / mingw 的情况下交叉编译到 windows。
package lzfse

import (
	"bytes"
	"io"

	pure "apfslens.local/third_party/lzgo"
)

// DecodeBuffer 解码 LZFSE 流（bvx- 系列块）。
func DecodeBuffer(src []byte) []byte {
	if len(src) < 4 {
		return nil
	}
	out, err := io.ReadAll(pure.NewReader(bytes.NewReader(src)))
	if err != nil {
		return nil
	}
	return out
}

// DecodeLZVNBuffer 解码裸 LZVN 数据；不支持时返回 0，调用方按失败处理。
func DecodeLZVNBuffer(src, dst []byte) uint {
	if len(src) >= 4 {
		switch string(src[:4]) {
		case "bvxn", "bvx1", "bvx2", "bvx-":
			out := DecodeBuffer(src)
			if len(out) > 0 {
				n := copy(dst, out)
				return uint(n)
			}
		}
	}
	return 0
}

// LzBitMapDecompressLen 尚未实现，返回失败状态。
func LzBitMapDecompressLen(src, dst []byte) (int, int) {
	return -1, 0
}

// EncodeBuffer / EncodeLZVNBuffer 仅占位。
func EncodeBuffer(src []byte) []byte        { return nil }
func EncodeLZVNBuffer(src, dst []byte) uint { return 0 }

// DecodeBufferInto 解码到预分配缓冲区。
func DecodeBufferInto(src, dst []byte) int {
	out := DecodeBuffer(src)
	if len(out) == 0 {
		return 0
	}
	if len(out) > len(dst) {
		out = out[:len(dst)]
	}
	copy(dst, out)
	return len(out)
}

func DecodeScratchSize() uint                              { return 0 }
func EncodeScratchSize() uint                              { return 0 }
func DecodeBufferWithScratch(src, dst, scratch []byte) int { return DecodeBufferInto(src, dst) }
