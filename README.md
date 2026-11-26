# U APFS 工具（U盘格式化工具）

Windows 上的磁盘工具：读 APFS 盘（含 FileVault 加密卷）、浏览/预览/导出文件、把 APFS 卷「挂载到此电脑」成为盘符、以及 U 盘/SD 卡的常规格式化与分区表操作。

界面是 WebView2 承载的本地页面（内嵌 `ui.html`），后端用 Go 解析 APFS，另有两个原生模块分担搬运与分区核对，Python 负责诊断报告排版。

## 仓库结构

```
src/                 Go 主程序（module udisk）
  ui.html            界面（内嵌进 exe）
  apfsx.go           APFS 只读解析：容器 / 卷 / 目录 / 文件 / 快照
  apfsapi.go         卷扫描、遍历、缓存
  parttable.go       GPT / MBR / Apple 分区图自解析
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
releases/<版本>/     三架构主程序 + 原生模块 + 使用说明
```

## 构建

需要 Go 1.26+（`GOTOOLCHAIN=go1.27.1`），Windows 交叉编译无需 Visual Studio。

```bash
cd src
GOTOOLCHAIN=go1.27.1 GOFLAGS=-mod=mod GOOS=windows GOARCH=amd64 \
  go build -ldflags "-H windowsgui -s -w" -o ../udisk-tool-x64.exe .
# 386 / arm64 同理换 GOARCH
```

原生模块（Linux 上交叉编译）：

```bash
x86_64-w64-mingw32-g++-posix -O2 -std=c++11 -static -static-libgcc -static-libstdc++ \
  -o udisk_native.exe native/native.cpp
i686-w64-mingw32-g++-posix  ... -o udisk_native-x86.exe native/native.cpp
x86_64-w64-mingw32-gcc-posix -O2 -static -o udisk_probe.exe native/probe.c
i686-w64-mingw32-gcc-posix   -O2 -static -o udisk_probe-x86.exe native/probe.c
```

`make` 里有对应目标（见 `Makefile`）。

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
