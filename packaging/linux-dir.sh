#!/usr/bin/env bash
# 打两个 Linux 目录（含图标与 .desktop），可直接 tar 分发。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
for ARCH in x64 arm64; do
  BIN="$ROOT/build/udisk-tool-linux-$ARCH"
  [ -x "$BIN" ] || { echo "缺少 $BIN，请先 make linux"; exit 1; }
  D="$ROOT/build/linux-$ARCH"
  rm -rf "$D"; mkdir -p "$D"
  cp "$BIN" "$D/udisk-tool-linux-$ARCH"
  cp "$ROOT/assets/png/icon-512.png" "$D/udisk-tool.png"
  sed "s/Exec=.*/Exec=udisk-tool-linux-$ARCH/" "$ROOT/packaging/udisk-tool.desktop" > "$D/udisk-tool.desktop"
  chmod +x "$D/udisk-tool-linux-$ARCH"
  echo "已生成 $D"
done
