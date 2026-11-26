#!/usr/bin/env bash
# 在 macOS 本机把 darwin 二进制打包成 .app（Linux 构建机只负责出二进制）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="$ROOT/dist/U APFS Tool.app"
ARCH="${1:-arm64}"
BIN="$ROOT/udisk-tool-macos-${ARCH}"
[ -x "$BIN" ] || { echo "缺少 $BIN，请先 make darwin"; exit 1; }

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/udisk-tool"
chmod +x "$APP/Contents/MacOS/udisk-tool"
cp "$ROOT/packaging/Info.plist" "$APP/Contents/Info.plist"
cp "$ROOT/assets/icon.icns" "$APP/Contents/Resources/icon.icns"
echo "已生成 $APP"
