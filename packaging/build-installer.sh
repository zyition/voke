#!/usr/bin/env bash
# ===========================================================================
# 构建 voke 安装器（在 WSL 里跑）
# ---------------------------------------------------------------------------
# 流程：
#   1) 用 Go 交叉构建 Windows GUI 版 voke.exe（注入版本号，可选日志上限）
#   2) 调 Windows 侧 ISCC.exe 编译 packaging/voke.iss（产物落 packaging/dist/）
#   3) 打印产物路径与 sha256
#
# 可选环境变量：
#   VOKE_VERSION         版本号（三段数字，可带 v）。未设时取最近一个 vX.Y.Z tag；
#                        同时注入设置页「关于」与安装器 AppVersion。
#   VOKE_LOG_MAX_LINES   日志行数上限（默认 9999）。通过 -ldflags -X 注入
#                        internal/platform.logMaxLinesRaw，不是运行时环境变量。
#   ISCC                 ISCC.exe 路径（找不到时自动搜索常见安装位置）
#
# 幂等：Go 走构建缓存、ISCC 覆盖旧产物；dist/build 每次重建。
# 前置：Go 工具链；Windows 侧已装 Inno Setup 7
#       （winget install --id JRSoftware.InnoSetup.7 -e）。
# ===========================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$REPO_ROOT/poc/voke-go"
ISS="$REPO_ROOT/packaging/voke.iss"
DIST="$REPO_ROOT/packaging/dist"
BUILD_DIR="$DIST/build"
EXE="$BUILD_DIR/voke.exe"

# --- 1) 版本号 -------------------------------------------------------------
VERSION="${VOKE_VERSION:-}"
if [ -z "$VERSION" ]; then
  TAG="$(git -C "$REPO_ROOT" describe --tags --abbrev=0 2>/dev/null || true)"
  VERSION="${TAG#v}"
fi
VERSION="${VERSION#v}"
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "错误：版本号必须是三段数字（如 0.2.3），当前为 '$VERSION'。" >&2
  echo "      可用 VOKE_VERSION=0.2.3 bash packaging/build-installer.sh 指定。" >&2
  exit 1
fi
echo "== 版本：$VERSION =="
if ! git -C "$REPO_ROOT" diff --quiet || ! git -C "$REPO_ROOT" diff --cached --quiet; then
  echo "警告：工作区存在未提交改动，安装包将包含当前工作区代码。" >&2
fi

# --- 2) Go 交叉构建 --------------------------------------------------------
echo "== [1/3] 构建 voke.exe（Go，windows/amd64）=="
mkdir -p "$BUILD_DIR"
LDFLAGS="-s -w -H windowsgui"
LDFLAGS="$LDFLAGS -X github.com/zyition/voke/poc/voke-go/internal/app.buildVersion=$VERSION"
if [ -n "${VOKE_LOG_MAX_LINES:-}" ]; then
  if ! [[ "$VOKE_LOG_MAX_LINES" =~ ^[0-9]+$ ]] || [ "$VOKE_LOG_MAX_LINES" -le 0 ]; then
    echo "错误：VOKE_LOG_MAX_LINES 必须是正整数，当前为 '$VOKE_LOG_MAX_LINES'。" >&2
    exit 1
  fi
  echo "   日志行数上限 = $VOKE_LOG_MAX_LINES"
  LDFLAGS="$LDFLAGS -X github.com/zyition/voke/poc/voke-go/internal/platform.logMaxLinesRaw=$VOKE_LOG_MAX_LINES"
else
  echo "   日志行数上限 = 9999（默认）"
fi

(
  cd "$GO_DIR"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -tags mygo_noinspector -ldflags="$LDFLAGS" -o "$EXE" .
)
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
    "/mnt/c/Program Files/Inno Setup 7/ISCC.exe"; do
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
echo "== [3/3] 编译安装器（$ISS）=="
ISS_WIN="$(wslpath -w "$ISS")"
EXE_WIN="$(wslpath -w "$EXE")"
"$ISCC_EXE" "$ISS_WIN" "-dVokeVersion=$VERSION" "-dVokeExe=$EXE_WIN"

# --- 5) 打印产物 ----------------------------------------------------------
OUT="$DIST/voke-setup-$VERSION.exe"
if [ ! -f "$OUT" ]; then
  echo "错误：ISCC 已结束但未找到 $OUT" >&2
  exit 1
fi

echo
echo "== 完成 =="
echo "产物：$OUT"
echo "大小：$(stat -c '%s' "$OUT") 字节"
echo "sha256：$(sha256sum "$OUT" | awk '{print $1}')"
