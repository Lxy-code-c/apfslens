module udiskx

go 1.27.1

require (
	github.com/blacktop/go-apfs v1.0.33
	golang.org/x/sys v0.42.0
)

require (
	github.com/VividCortex/ewma v1.2.0 // indirect
	github.com/acarl005/stripansi v0.0.0-20180116102854-5a71ef0e047d // indirect
	github.com/blacktop/lzfse-cgo v1.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/vbauerster/mpb/v7 v7.5.3 // indirect
	udisk.local/third_party/lzgo v0.0.0 // indirect
)

replace github.com/blacktop/lzfse-cgo => ./lzshim

replace udisk.local/third_party/lzgo => ./third_party/lzgo
