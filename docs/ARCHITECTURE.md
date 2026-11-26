# 代码结构

```
src/windows/        Windows 版（Go + WebView2 原生窗口，编译进 exe）
src/cross/          Linux / macOS 版（纯 Go，无 cgo，系统浏览器当窗口）
native/             Windows 原生辅助引擎源码（C++ 并行搬运 / C 分区核对）
tools/              apfslens_doctor.py 诊断排版、mk-test-image 测试镜像生成
packaging/          .app 打包脚本、Info.plist、Linux .desktop
assets/icons/       图标：.ico（Windows）/ .icns（macOS）/ png（Linux、文档）
docs/               使用说明与结构说明（本目录）
build/              构建产物（不入库）
```

## 两条产品线为什么分成两个 Go module

- `src/windows`：要挂 WebView2（cgo + COM），只能 Windows 上编译，依赖 `third_party/webview2`（被改过：库里 5 处 `log.Fatal` 改成记录错误，避免窗口出现后进程直接退出）。
- `src/cross`：故意不碰 cgo，任何平台交叉编译都能出包；界面通过 `127.0.0.1` 上的本地 HTTP 提供给系统浏览器，窗口就是浏览器窗口。

两边共享同一套解析代码（`apfsx.go`、`parttable.go`、`extents_help.go`，跨平台版是一份拷贝），改解析逻辑要两边同步。

## 引擎分工

| 引擎 | 文件 | 职责 |
|---|---|---|
| Go 主引擎 | `apfsx.go` / `apfsapi.go` | 分区表、容器、卷、目录、文件、快照全部自解析 |
| C++ 搬运引擎 | `native/native.cpp` → `apfslens_native.exe` | >8MB 且未加密未压缩的文件，按 Go 算好的物理段多线程拷贝 |
| C 核对引擎 | `native/probe.c` → `apfslens_probe.exe` | 独立读 GPT/MBR、校验 CRC、交叉验证分区偏移 |
| Python 诊断 | `tools/apfslens_doctor.py` | 把 Go 收集的事实排版成建议；没有 Python 时用 Go 内置排版 |

三个辅助引擎都是**可选**：文件缺失时主程序自动退回 Go 路径，不报错。

## 几个必须记住的实现约束

- **APFS 块大小不是常量**：`go-apfs` 的 `types.BLOCK_SIZE` 包级默认是 0，必须先读 NXSB offset 36 的 `block_size`（4K–64K）再解析，否则第一次读就失败。
- **隐藏项在三处出口挡**：列目录（含被缓存的 `entries` 分支）、搜索/大文件/快照对比的遍历、下载接口（403）。新增任何列表接口都要走 `hidden()`。
- **容器缓存带引用计数**：`openTarget` 返回释放函数，`refs>0` 的条目不会被淘汰、文件不会被关，否则并发下会出现 `file already closed`。
- **本地 HTTP 只监听 127.0.0.1**，`guard` 中间件挡跨站 Origin 并把单个请求的 panic 转成 500，不让一次解析异常带走整个进程。

## 构建

```bash
make windows    # apfslens-x64.exe / -x86.exe / -arm64.exe
make linux      # apfslens-linux-x64 / -arm64
make darwin     # apfslens-macos-arm64 / -x64
make package    # Linux 目录（含 .desktop + png）+ macOS .app（含 icns）
make test       # Windows 侧单元测试 + 跨平台侧 vet
make vet        # 两侧 vet
```

Windows 原生辅助引擎需要 mingw-w64（`make native`）；本机没有也能出主程序，只是少了加速与交叉核对。

## 测试

`make test` 里需要一块真实 APFS 镜像（默认 `/tmp/apfs.img`，可用 `APFSLENS_TEST_IMAGE` 指定）：

```bash
go run ./tools/mk-test-image -src test.dmg -out /tmp/apfs.img
```

镜像可以是裸 APFS 容器，也可以是带 GPT 的整盘镜像，测试助手两种都认。
