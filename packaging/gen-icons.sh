#!/usr/bin/env bash
# ===========================================================================
# 生成 voke 全套图标资产（在 WSL / Linux 里跑）
# ---------------------------------------------------------------------------
# 唯一权威源：assets/voke-icon.svg
# 流程：
#   1) resvg 在临时目录渲染 2048² 母版（PNG，不入库）
#   2) Pillow 从母版生成 ICO 九档 + 128² 托盘 PNG 两份
#   3) windres 从 packaging/voke.rc + assets/voke.ico 生成 exe 图标资源 syso
#   4) 自检：托盘两份逐字一致、ICO 档位齐全、资产可逐字节复现
#
# 用法：
#   bash packaging/gen-icons.sh           生成并自检（就地覆盖资产）
#   bash packaging/gen-icons.sh --check   生成到临时目录，与现有资产逐字节比对（不写仓库）
#
# 前置（命令行工具优先用 mise 装）：
#   resvg                       mise use -g resvg@latest；也可用 RESVG=/abs/path 指定
#   python3 + Pillow            python3 -m pip install Pillow
#   x86_64-w64-mingw32-windres  系统 mingw-w64 包
#
# 注意：
#   - 渲染必须用 resvg：ImageMagick 内置 SVG delegate 会把透明区填白，只用于校验
#     位图，不要用它生成母版。
#   - 根目录 voke-icon-*.svg 是设计稿，不参与构建；换图先写进 assets/voke-icon.svg。
#   - 本脚本不进 CI：发版只消费已提交的资产，CI 不装 resvg / Pillow / windres。
# ===========================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SRC="assets/voke-icon.svg"
RC="packaging/voke.rc"

MASTER="voke-icon-master.png"
ICO="assets/voke.ico"
TRAY="assets/voke-tray.png"
TRAY_EMBED="internal/ui/assets/voke-tray.png"
SYSO="rsrc_windows_amd64.syso"

CHECK=0
case "${1:-}" in
  "") ;;
  --check) CHECK=1 ;;
  *) echo "用法：$0 [--check]" >&2; exit 2 ;;
esac

# --- 1) 依赖 ---------------------------------------------------------------
RESVG_BIN="${RESVG:-}"
if [ -z "$RESVG_BIN" ]; then
  RESVG_BIN="$(command -v resvg || true)"
fi
if [ -z "$RESVG_BIN" ] || [ ! -x "$RESVG_BIN" ]; then
  echo "错误：找不到 resvg。装：mise use -g resvg@latest，或用 RESVG=/abs/path 指定。" >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "错误：找不到 python3。" >&2
  exit 1
fi
if ! python3 -c 'import PIL' >/dev/null 2>&1; then
  echo "错误：python3 缺 Pillow：python3 -m pip install Pillow" >&2
  exit 1
fi
if ! command -v x86_64-w64-mingw32-windres >/dev/null 2>&1; then
  echo "错误：找不到 x86_64-w64-mingw32-windres（需要 mingw-w64 的 windres）。" >&2
  exit 1
fi

# --- 2) 生成到临时目录（--check 与正式生成共用同一条路径）------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/assets" "$TMP/internal/ui/assets" "$TMP/root"

echo "== [1/3] resvg 渲染 2048² 母版 =="
"$RESVG_BIN" -w 2048 -h 2048 "$SRC" "$TMP/$MASTER"

echo "== [2/3] Pillow 生成 ICO 九档 + 托盘 PNG 两份 =="
python3 - "$TMP" <<'PY'
import sys
from pathlib import Path
from PIL import Image

out = Path(sys.argv[1])
master = Image.open(out / "voke-icon-master.png").convert("RGBA")
# Windows 图标档位。20/40 别删：125%/150% DPI 的任务栏与资源管理器会直接取这两档。
SIZES = [(16, 16), (20, 20), (24, 24), (32, 32), (40, 40),
         (48, 48), (64, 64), (128, 128), (256, 256)]
master.save(out / "assets/voke.ico", format="ICO", sizes=SIZES)
tray = master.resize((128, 128), Image.LANCZOS)
tray.save(out / "assets/voke-tray.png", "PNG")
tray.save(out / "internal/ui/assets/voke-tray.png", "PNG")
PY

echo "== [3/3] windres 生成 syso =="
x86_64-w64-mingw32-windres -I "$TMP/assets" -i "$RC" -O coff -o "$TMP/root/$SYSO"

# --- 3) 自检（生成物本身）--------------------------------------------------
echo "== 自检 =="
if ! cmp -s "$TMP/$TRAY" "$TMP/$TRAY_EMBED"; then
  echo "错误：托盘两份不一致" >&2
  exit 1
fi
echo "  托盘两份逐字一致"
python3 - "$TMP/$ICO" <<'PY'
import struct, sys
d = open(sys.argv[1], "rb").read()
n = struct.unpack_from("<H", d, 4)[0]
sizes = [d[6 + 16 * i] or 256 for i in range(n)]
want = [16, 20, 24, 32, 40, 48, 64, 128, 256]
if sizes != want:
    sys.exit(f"错误：ICO 档位不符：{sizes}，应为 {want}")
print(f"  ICO {n} 档：{'/'.join(map(str, sizes))}")
PY

# --- 4) 落地 或 比对 -------------------------------------------------------
if [ "$CHECK" -eq 1 ]; then
  echo "== --check：与现有资产逐字节比对（不写仓库）=="
  fail=0
  for rel in "$ICO" "$TRAY" "$TRAY_EMBED" "$SYSO"; do
    if [ "$rel" = "$SYSO" ]; then
      src="$TMP/root/$SYSO"
    else
      src="$TMP/$rel"
    fi
    if cmp -s "$src" "$rel"; then
      echo "  OK    $rel"
    else
      echo "  差异  $rel" >&2
      fail=1
    fi
  done
  if [ "$fail" -ne 0 ]; then
    echo "== FAIL：现有资产无法由 $SRC 复现，请跑 bash packaging/gen-icons.sh 重新生成 ==" >&2
    exit 1
  fi
  echo "== PASS：全部资产可逐字节复现 =="
else
  cp "$TMP/$ICO" "$ICO"
  cp "$TMP/$TRAY" "$TRAY"
  cp "$TMP/$TRAY_EMBED" "$TRAY_EMBED"
  cp "$TMP/root/$SYSO" "$SYSO"
  echo "== 生成完成 =="
  for rel in "$ICO" "$TRAY" "$TRAY_EMBED" "$SYSO"; do
    echo "  $rel"
  done
  echo "提示：资产改动需随源码一并提交；发版 CI 只消费已提交的资产，不会跑本脚本。"
fi
