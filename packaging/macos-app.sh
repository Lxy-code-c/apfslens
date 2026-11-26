#!/usr/bin/env bash
# 把 darwin 二进制打包成 .app（Linux 构建机也能打，图标已入库）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VER="${1:-1.0.0}"
for ARCH in arm64 x64; do
  BIN="$ROOT/build/apfslens-macos-$ARCH"
  [ -x "$BIN" ] || { echo "缺少 $BIN，请先 make darwin"; exit 1; }
  APP="$ROOT/build/mac-$ARCH/ApfsLens.app"
  rm -rf "$APP"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  cp "$BIN" "$APP/Contents/MacOS/ApfsLens"
  chmod +x "$APP/Contents/MacOS/ApfsLens"
  sed "s/@@VER@@/$VER/g" "$ROOT/packaging/Info.plist" > "$APP/Contents/Info.plist"
  cp "$ROOT/assets/icons/icon.icns" "$APP/Contents/Resources/icon.icns"
  echo "已生成 $APP"
done
