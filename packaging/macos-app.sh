#!/usr/bin/env bash
# 把 darwin 二进制打包成 .app（Linux 构建机也能打，图标已入库）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VER="${1:-2026Y11M26D-4}"
for ARCH in arm64 x64; do
  BIN="$ROOT/build/udisk-tool-macos-$ARCH"
  [ -x "$BIN" ] || { echo "缺少 $BIN，请先 make darwin"; exit 1; }
  APP="$ROOT/build/mac-$ARCH/U APFS.app"
  rm -rf "$APP"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  cp "$BIN" "$APP/Contents/MacOS/UAPFS"
  chmod +x "$APP/Contents/MacOS/UAPFS"
  sed "s/@@VER@@/$VER/g" "$ROOT/packaging/Info.plist" > "$APP/Contents/Info.plist"
  cp "$ROOT/assets/icon.icns" "$APP/Contents/Resources/icon.icns"
  echo "已生成 $APP"
done
