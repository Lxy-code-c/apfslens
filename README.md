# ApfsLens

> ApfsLens：一个能“看穿” Mac 格式磁盘的图形工具。插上 Mac 格式化过的盘，双击就能进去翻文件。
> 名字里的 Lens 就是放大镜 —— 把盘当透明的，一眼看进去。

Windows / Linux / macOS 上的 APFS 只读工具：选一块盘（或磁盘镜像）→ 看里面的卷 → 进目录 → 预览/下载文件。
**三个平台都是双击即开的图形程序，不提供命令行版本。**

界面是本地页面（只监听 `127.0.0.1`，不联网），后端用 Go 自己解析 APFS：容器、卷、目录、文件、快照全部自解析，不依赖系统装载。

## 功能

- 自动识别 GPT / MBR / Apple 分区表，找出 APFS 分区（也支持直接给整块 APFS 分区或镜像文件）
- 卷列表（名称、角色、密封、加密、是否可用）、目录浏览、文件下载、卷内搜索、快照列表
- 隐藏项（`.` 开头，如 `.DS_Store`、`._*`）在任何列表、搜索结果、下载里都不出现
- Windows 版另有：格式化 / APFS 模板还原、盘符映射「挂载到此电脑」、FileVault 解锁、一键诊断
- 高分屏友好：Windows 版启动即声明每显示器 DPI 感知（125%~250% 缩放下按物理像素渲染，不会整屏被拉伸变糊），三平台界面字体、表格、图标均按清晰优先排版

## 仓库结构

```
src/                 Windows 版（Go + WebView2 原生窗口，内嵌 ui.html）
  rsrc_windows_*.syso  程序图标资源（由 assets/icon.ico 生成，已入库）
src/cross/           Linux / macOS 图形版（纯 Go，无 cgo）
  apfsx.go             APFS 只读解析：容器 / 卷 / 目录 / 文件 / 快照
  parttable.go         GPT / MBR / APM 自解析
  api.go               本地 HTTP 接口（扫描 / 卷 / 目录 / 下载 / 搜索 / 快照）
  ui.html              图形界面
native/              Windows 原生辅助模块：C++ 字节搬运、C 分区核对（源码）
tools/apfslens_doctor.py Python 诊断报告（Windows 版用）
packaging/           .app 打包脚本、Info.plist、Linux .desktop
assets/              程序图标：icon.ico / icon.icns / png/icon-*.png / icon-master.png
releases/<版本>/     各版本二进制与使用说明（版本号见 CHANGELOG，当前 1.0.0）
```

## 构建

需要 Go 1.26+（`GOTOOLCHAIN=go1.27.1`）。

```bash
make windows    # Windows x64 / x86 / ARM64（图标已内嵌）
make linux      # Linux  x64 / arm64
make darwin     # macOS  Apple Silicon / x64
make package    # 打 Linux 目录（含 .desktop + png）+ macOS .app（含 icns）
make native     # Windows 原生辅助模块（需要 mingw-w64）
make test       # 单元测试
```

Windows 图标资源已入库；改图标后执行 `make icons` 重新生成 `.syso`。

## 各平台形态

| 平台 | 产物 | 打开方式 |
|------|------|----------|
| Windows | `apfslens-x64.exe` / `-x86.exe` / `-arm64.exe` | 双击；或用 `launch.cmd`（按 CPU 自动选架构、请求管理员权限） |
| Linux | `apfslens-linux-x64`（含 `apfslens.desktop`、`apfslens.png`） | 双击桌面入口或可执行文件 |
| macOS | `ApfsLens.app`（Finder 里显示为「ApfsLens」，Apple Silicon / Intel 各一份） | 双击，或拖进「应用程序」 |

Linux / macOS 版窗口用系统默认浏览器渲染（`127.0.0.1` 随机端口，仅本机可访问），所以不依赖任何 WebView 运行时。读整块盘需要权限：Linux 用 sudo 启动一次或加入 `disk` 组；macOS 首次会弹「完全磁盘访问」，按提示允许。读镜像文件不需要任何权限。

## 能力边界

- 只读：不改动原盘，不做写操作
- 跨平台版不支持格式化、写盘、盘符映射（这三项只在 Windows 版）
- 跨平台版对加密卷只显示「加密」标签，不解锁；FileVault 解锁在 Windows 版

## 图标

`assets/` 一套设计三种格式：`icon.ico`（Windows，16–256 多尺寸）、`icon.icns`（macOS，含 512/1024）、`png/icon-*.png`（Linux 桌面与文档）、`icon-master.png`（1024 母版）。图形语义：银色盘片 + 蓝色放大镜 = 查看磁盘里的内容。

## License

MIT，见 `LICENSE`。
