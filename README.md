# U APFS 工具（U盘格式化工具）

**纯图形版，不提供命令行。** 目前 Windows 功能完整：读 APFS 盘（含 FileVault 加密卷）、浏览/预览/导出文件、把 APFS 卷「挂载到此电脑」成为盘符、以及 U 盘/SD 卡的常规格式化与分区表操作。Linux / macOS 为预览版本：图形界面壳与 APFS 只读解析引擎已随包编译，磁盘扫描、挂载、格式化等平台专属能力将在后续版本接通。

界面是本地承载的页面（内嵌 `ui.html`），后端用 Go 解析 APFS，另有两个原生模块分担搬运与分区核对，Python 负责诊断报告排版。

## 仓库结构

```
src/                 Go 主程序（module udisk）
  ui.html            界面（内嵌进程序）
  apfsx.go           APFS 只读解析：容器 / 卷 / 目录 / 文件 / 快照
  apfsapi.go         卷扫描、遍历、缓存
  parttable.go       GPT / MBR / Apple 分区图自解析
  gui_windows.go     Windows 原生窗口（WebView2）
  gui_other.go       Linux / macOS 图形壳（本机页面 + 系统浏览器）
  device_windows.go  原始设备读写（含 4K 对齐）
  mount_windows.go   无驱动盘符映射（DefineDosDevice）
  format_windows.go  格式化任务与 APFS 模板还原
  rpc_windows.go     全部前后端接口
  third_party/       vendor 的 webview2 修补版、纯 Go lzfse
  lzshim/            冒充 lzfse-cgo 的兼容层
native/
  native.cpp         C++ 搬运引擎（多线程，逐 extent 拷贝）
  probe.c            C 分区核对引擎（独立解析 GPT/MBR，校验 CRC）
tools/
  udisk_doctor.py    Python 诊断报告
assets/              程序图标：icon.ico / icon.icns / 各尺寸 PNG
packaging/           macOS .app 打包脚本、Info.plist、Linux .desktop
releases/<版本>/     主程序 + 原生模块 + 使用说明
```

## 构建

需要 Go 1.26+（`GOTOOLCHAIN=go1.27.1`）。所有目标都是图形版，不含命令行子命令。

```bash
make windows    # Windows: x64 / x86 / ARM64
make linux      # Linux:   x64 / arm64（预览壳）
make darwin     # macOS:   arm64（Apple Silicon）/ x64（Intel）（预览壳）
make native     # Windows 原生辅助模块（C++ / C，需 mingw）
make test       # 单元测试
make package    # 在 macOS 本机把 darwin 二进制打成 .app
```

手动单平台编译示例：

```bash
cd src
GOTOOLCHAIN=go1.27.1 GOFLAGS=-mod=mod GOOS=windows GOARCH=amd64 \
  go build -ldflags "-H windowsgui -s -w" -o ../udisk-tool-x64.exe .
```

macOS `.app` 打包：`make darwin` 后在 Mac 上执行 `bash packaging/macos-app.sh arm64`（或 `x64`），图标取自 `assets/icon.icns`。Linux 桌面入口见 `packaging/udisk-tool.desktop`，图标 PNG 在 `assets/png/`。

原生模块（Linux 上交叉编译）：

```bash
x86_64-w64-mingw32-g++-posix -O2 -std=c++11 -static -static-libgcc -static-libstdc++ \
  -o udisk_native.exe native/native.cpp
i686-w64-mingw32-g++-posix  ... -o udisk_native-x86.exe native/native.cpp
x86_64-w64-mingw32-gcc-posix -O2 -static -o udisk_probe.exe native/probe.c
i686-w64-mingw32-gcc-posix   -O2 -static -o udisk_probe-x86.exe native/probe.c
```

## 平台支持状态

| 平台 | 状态 | 说明 |
|------|------|------|
| Windows x64 / x86 / ARM64 | 完整 | 浏览、预览、导出、挂载映射、格式化、诊断 |
| Linux x64 / arm64 | 预览 | 图形壳可启动；磁盘访问与挂载待接入 |
| macOS arm64 / x64 | 预览 | 图形壳可启动；磁盘访问与挂载待接入 |

## 多引擎分工

| 引擎 | 职责 | 缺省时 |
|------|------|--------|
| Go | 界面 + APFS 解析（唯一解析实现，避免两套结果冲突） | — |
| C++ `udisk_native.exe` | >8MB 文件的批量字节搬运（4 线程 / 8MB 缓冲） | 回退 Go 读路径 |
| C `udisk_probe.exe` | 独立读 GPT/MBR、校验 CRC、找 APFS、读 NXSB 块大小，交叉核对 | 跳过该道判断 |
| Python `udisk_doctor.py` | 诊断报告排版与建议 | 回退 Go 内置排版 |

安全闸门：加密卷、压缩文件（decmpfs）、数据段总长 ≠ 文件大小的文件，一律不走 C++ 搬运，避免拷出乱码。

## 运行

把 `releases/<版本>/` 里的文件放在同一目录，双击「launch.cmd」（会按 CPU 自动选 x64 / x86 / ARM64，并请求管理员权限）。缺任何一个辅助模块都不会崩，只是对应能力退回上一级实现。

## 已知限制

- 只读：不写 APFS 卷（挂载出来的盘符写入只改镜像，不回写原盘）。
- 盘符映射用 `DefineDosDevice`，不需要装 WinFsp/Dokan 驱动；个别杀软/组策略会拦截，界面会标「盘符未生效」。
- 文件系统里以 `.` 开头的隐藏项（`.DS_Store`、`._*` 资源叉等）默认不展示、不导出。

## License

MIT，见 `LICENSE`。
