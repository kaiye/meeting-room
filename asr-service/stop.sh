#!/bin/bash
# 停止服务并释放模型占用的内存。
if pgrep -f funasr_openai_server.py >/dev/null; then
  pkill -f funasr_openai_server.py && echo "✅ 已停止"
else
  echo "ℹ️  服务本来就没在运行"
fi
