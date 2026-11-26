module apfslens

go 1.27.1

require (
	github.com/blacktop/go-apfs v1.0.33
	github.com/go-fde/apfs v0.5.1
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	golang.org/x/sys v0.50.0
)

require (
	github.com/VividCortex/ewma v1.2.0 // indirect
	github.com/acarl005/stripansi v0.0.0-20180116102854-5a71ef0e047d // indirect
	github.com/apex/log v1.9.0 // indirect
	github.com/blacktop/go-plist v1.0.2 // indirect
	github.com/blacktop/lzfse-cgo v1.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-encryptions/kdfbudget v0.1.0 // indirect
	github.com/go-encryptions/xts v0.3.2 // indirect
	github.com/go-simd/adler32 v0.3.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/ulikunitz/xz v0.5.17 // indirect
	github.com/vbauerster/mpb/v7 v7.5.3 // indirect
	github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	apfslens.local/third_party/lzgo v0.0.0 // indirect
)

replace github.com/blacktop/lzfse-cgo => ./lzshim

replace apfslens.local/third_party/lzgo => ./third_party/lzgo

replace github.com/jchv/go-webview2 => ./third_party/webview2
