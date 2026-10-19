#!/usr/bin/env bash
# ===========================================================================
# 构建 voke 安装器（在 WSL 里跑）
# ---------------------------------------------------------------------------
# 流程：
#   1) 前置真实的 clang / lld / llvm-tools 到 PATH（绕开 mise shim）
#   2) cargo xwin build --release --target x86_64-pc-windows-msvc -p voke-app
#   3) 调 Windows 侧 ISCC.exe 编译 packaging/voke.iss（产物落 packaging/dist/）
#   4) 打印产物路径与 sha256
#
# 可选环境变量：
#   VOKE_LOG_MAX_LINES  日志行数上限（默认 9999，语义见 voke-sys/src/logcap.rs：
#                       跨启动追加 + 超限从头部删一批最旧的行）。环境变量由 cargo
#                       子进程自动继承，这里只做回显；如
#                       VOKE_LOG_MAX_LINES=20000 bash packaging/build-installer.sh
#   ISCC                ISCC.exe 路径（找不到时指定）
#
# 幂等：可重复执行（cargo 走增量、ISCC 覆盖旧产物、dist 目录按需创建）。
# 前置：Windows 侧已装 Inno Setup 7（winget install --id JRSoftware.InnoSetup.7 -e）。
#       找不到 ISCC 时可用环境变量指定：ISCC=/path/to/ISCC.exe ./build-installer.sh
# ===========================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TARGET="x86_64-pc-windows-msvc"
EXE="$REPO_ROOT/target/$TARGET/release/voke.exe"
ISS="$REPO_ROOT/packaging/voke.iss"
DIST="$REPO_ROOT/packaging/dist"

# --- 1) PATH 前置 ---------------------------------------------------------
# cc-rs 调用的 clang-cl / llvm-lib 会命中 mise shim 并报 "No version is set for
# shim"，所以把真实工具链的 bin 前置进 PATH（见 docs/HISTORY.md）。
# 版本号随 mise 安装变化，用 mise 维护的 latest 符号链接，避免把版本写死。
MISE_INSTALLS="$HOME/.local/share/mise/installs"
prepend_bin() {
  if [ -d "$1/bin" ]; then
    PATH="$1/bin:$PATH"
  else
    echo "警告：找不到 $1/bin，已跳过（若构建报缺 clang-cl/llvm-lib，检查对应 mise 工具是否安装）" >&2
  fi
}
prepend_bin "$MISE_INSTALLS/clang/latest"
prepend_bin "$MISE_INSTALLS/conda-lld/latest"
prepend_bin "$MISE_INSTALLS/conda-llvm-tools/latest"
export PATH

# --- 2) 构建 exe ----------------------------------------------------------
echo "== [1/3] 构建 voke.exe（cargo xwin release, $TARGET）=="
echo "   clang-cl = $(command -v clang-cl || echo '(未找到!)')"
# 日志行数上限透传：VOKE_LOG_MAX_LINES 由 cargo 子进程自动继承给 build.rs，
# 未设时用默认 9999（行为不变）。这里只做回显，避免打包参数不可见。
if [ -n "${VOKE_LOG_MAX_LINES:-}" ]; then
  echo "   VOKE_LOG_MAX_LINES = $VOKE_LOG_MAX_LINES（日志行数上限，来自环境变量）"
else
  echo "   VOKE_LOG_MAX_LINES 未设，用默认 9999（日志行数上限）"
fi
cargo xwin build --release --target "$TARGET" -p voke-app

if [ ! -f "$EXE" ]; then
  echo "错误：构建后仍未找到产物 $EXE" >&2
  exit 1
fi

# --- 3) 定位 Windows 侧 ISCC.exe -----------------------------------------
echo "== [2/3] 定位 Inno Setup 编译器（ISCC.exe）=="
find_iscc() {
  if [ -n "${ISCC:-}" ] && [ -x "${ISCC:-}" ]; then
    printf '%s\n' "$ISCC"; return 0
  fi
  local shim
  shim="$(command -v iscc.exe 2>/dev/null || true)"
  if [ -n "$shim" ] && [ -x "$shim" ]; then
    printf '%s\n' "$shim"; return 0
  fi
  local u c
  for u in /mnt/c/Users/*; do
    c="$u/AppData/Local/Programs/Inno Setup 7/ISCC.exe"
    [ -x "$c" ] && { printf '%s\n' "$c"; return 0; }
  done
  for c in \
    "/mnt/c/Program Files (x86)/Inno Setup 7/ISCC.exe" \
    "/mnt/c/Program Files/Inno Setup 7/ISCC.exe" \
    "/mnt/c/Program Files (x86)/Inno Setup 6/ISCC.exe" \
    "/mnt/c/Program Files/Inno Setup 6/ISCC.exe"; do
    [ -x "$c" ] && { printf '%s\n' "$c"; return 0; }
  done
  return 1
}

ISCC_EXE="$(find_iscc || true)"
if [ -z "$ISCC_EXE" ]; then
  echo "错误：找不到 ISCC.exe。" >&2
  echo "  请先安装 Inno Setup 7： winget install --id JRSoftware.InnoSetup.7 -e" >&2
  echo "  或用环境变量指定：     ISCC='C:\\path\\to\\ISCC.exe' $0" >&2
  exit 1
fi
echo "   ISCC = $ISCC_EXE"
echo "   版本 = $("$ISCC_EXE" --version 2>/dev/null | tr -d '\r' || echo '(取版本失败)')"

# --- 4) 编译安装器 --------------------------------------------------------
echo "== [3/3] 编译安装器（{$ISS}）=="
mkdir -p "$DIST"
# ISCC 是 Windows 程序，脚本路径要转成 Windows 形式（\\wsl.localhost\...）；
# .iss 里的相对路径（Source/OutputDir/SetupIconFile）均以该脚本所在目录为基准。
ISS_WIN="$(wslpath -w "$ISS")"
"$ISCC_EXE" "$ISS_WIN"

# --- 5) 打印产物 ----------------------------------------------------------
OUT="$(ls -t "$DIST"/*.exe 2>/dev/null | head -n1 || true)"
if [ -z "$OUT" ]; then
  echo "错误：ISCC 已结束但 $DIST 下没有 .exe 产物" >&2
  exit 1
fi

echo
echo "== 完成 =="
echo "产物：$OUT"
echo "大小：$(ls -l "$OUT" | awk '{print $5}') 字节"
echo "sha256：$(sha256sum "$OUT" | awk '{print $1}')"
