#!/usr/bin/env bash
# 打两个 Linux 目录（含图标与 .desktop），可直接 tar 分发。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
for ARCH in x64 arm64; do
  BIN="$ROOT/build/apfslens-linux-$ARCH"
  [ -x "$BIN" ] || { echo "缺少 $BIN，请先 make linux"; exit 1; }
  D="$ROOT/build/linux-$ARCH"
  rm -rf "$D"; mkdir -p "$D"
  cp "$BIN" "$D/apfslens-linux-$ARCH"
  cp "$ROOT/assets/icons/png/icon-512.png" "$D/apfslens.png"
  sed "s/Exec=.*/Exec=apfslens-linux-$ARCH/" "$ROOT/packaging/apfslens.desktop" > "$D/apfslens.desktop"
  chmod +x "$D/apfslens-linux-$ARCH"
  echo "已生成 $D"
done
