# ApfsLens

**把一个 Mac 格式化过的盘，当透明的来看。**

插上盘、双击打开，就能看到里面的卷、目录和文件，想留哪个导出哪个。Windows / Linux / macOS 三平台都是图形程序，没有命令行入口，也不需要装任何 WebView 或驱动。

界面只是本地页面（只监听 `127.0.0.1`，不联网），后端用 Go 自己解析 APFS：容器、卷、目录、文件、快照全部自解析。

## 能做什么

- 自动识别 GPT / MBR / Apple 分区表，找出 APFS 分区（也支持直接给整块分区或镜像文件）
- 卷列表（名称、角色、密封、加密、是否可用）、目录浏览、文件导出、卷内搜索、快照浏览
- 隐藏项（`.` 开头，如 `.DS_Store`、`._*`）在列表、搜索、导出三处全部屏蔽
- 高分屏友好：Windows 启动即声明每显示器 DPI 感知，125%–250% 缩放下按物理像素渲染
- Windows 版另有：格式化 / APFS 模板还原、盘符映射「挂载到此电脑」、FileVault 解锁、一键诊断

## 仓库结构

```
src/windows/     Windows 版（Go + WebView2 原生窗口，界面与图标编进 exe）
src/cross/       Linux / macOS 版（纯 Go 无 cgo，系统浏览器当窗口）
native/          Windows 辅助引擎源码：C++ 并行搬运、C 分区交叉核对
tools/           诊断报告排版脚本、测试镜像生成器
packaging/       .app 打包脚本、Info.plist、Linux .desktop
assets/icons/    一套图标三种格式：.ico / .icns / png
docs/            USAGE.md（使用说明）、ARCHITECTURE.md（结构与实现约束）
```

细节见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)，使用与排障见 [docs/USAGE.md](docs/USAGE.md)。

## 构建

需要 Go 1.27（`GOTOOLCHAIN=go1.27.1`）。

```bash
make windows    # apfslens-x64.exe / apfslens-x86.exe / apfslens-arm64.exe
make linux      # apfslens-linux-x64 / apfslens-linux-arm64
make darwin     # apfslens-macos-arm64 / apfslens-macos-x64
make package    # Linux 目录（.desktop + png）+ macOS .app（.icns）
make test       # 单元测试（需要 APFS 测试镜像，见下）
make vet        # 两侧 go vet
make native     # Windows 辅助引擎（需要 mingw-w64，可选）
```

测试镜像可以自己生成（来源是一份 macOS 的 dmg）：

```bash
go run ./tools/mk-test-image -src test.dmg -out /tmp/apfs.img
```

## 版本

语义化版本：`1.0.0` → `1.0.1` → `1.0.2` …… 每个版本都有 GitHub Release 与 [CHANGELOG](CHANGELOG.md) 记录。

## License

MIT，见 [LICENSE](LICENSE)。
