#!/bin/bash
# 安装：前置检查 + 创建虚拟环境 + 装依赖。可重复执行。
set -e
cd "$(dirname "$0")"

echo "=== 1/3 前置检查 ==="
command -v ffmpeg >/dev/null || { echo "❌ 缺 ffmpeg：macOS 'brew install ffmpeg'，Debian 'sudo apt install -y ffmpeg'"; exit 1; }
echo "  ✅ ffmpeg $(ffmpeg -version 2>/dev/null | head -1 | awk '{print $3}')"

PY=$(command -v python3.11 || command -v python3.12 || command -v python3.10 || command -v python3)
VER=$($PY -c 'import sys;print(f"{sys.version_info.major}.{sys.version_info.minor}")')
case "$VER" in
  3.10|3.11|3.12) echo "  ✅ Python $VER ($PY)" ;;
  *) echo "❌ Python $VER 不受支持，需要 3.10-3.12"; exit 1 ;;
esac

MEM=$(( $(sysctl -n hw.memsize 2>/dev/null || grep MemTotal /proc/meminfo | awk '{print $2*1024}') / 1073741824 ))
[ "$MEM" -ge 8 ] && echo "  ✅ 内存 ${MEM} GB" || { echo "❌ 内存仅 ${MEM} GB，至少需要 8 GB"; exit 1; }
[ "$MEM" -lt 16 ] && echo "  ⚠️  内存 ${MEM} GB 偏紧，只能跑 Nano 这一个引擎，别同时开大型应用"

DISK=$(df -Pk . | awk 'NR==2{print int($4/1048576)}')
[ "$DISK" -ge 5 ] && echo "  ✅ 可用磁盘 ${DISK} GB" || { echo "❌ 可用磁盘仅 ${DISK} GB，至少需要 5 GB"; exit 1; }

echo "=== 2/3 创建虚拟环境 ==="
if [ -d .venv ]; then
  echo "  ℹ️  .venv 已存在，跳过创建"
elif command -v uv >/dev/null; then
  uv venv --python "$VER" .venv && echo "  ✅ 用 uv 创建"
else
  "$PY" -m venv .venv && echo "  ✅ 用 venv 创建（没装 uv，会比较慢）"
fi

echo "=== 3/3 安装依赖（首次约 3-10 分钟）==="
PKGS='funasr==1.3.30 modelscope==1.38.1 torch==2.13.0 torchaudio soundfile fastapi==0.140.7 uvicorn==0.51.0 python-multipart'
if command -v uv >/dev/null; then
  uv pip install --python .venv/bin/python $PKGS
else
  .venv/bin/pip install --quiet --upgrade pip && .venv/bin/pip install $PKGS
fi

.venv/bin/python -c "import funasr, torch, fastapi" && echo "  ✅ 依赖就绪"
echo
echo "🎉 安装完成。下一步：./start.sh"
