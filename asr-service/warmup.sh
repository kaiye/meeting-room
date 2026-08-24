#!/bin/bash
# 预热：触发模型下载与加载。首次部署必做，否则客户端第一个请求要干等几分钟。
cd "$(dirname "$0")"
PORT=${PORT:-8100}
BASE="http://127.0.0.1:$PORT"

[ -f test.wav ] || ffmpeg -f lavfi -i anullsrc=r=16000:cl=mono -t 5 -y test.wav 2>/dev/null

echo "正在预热 fun-asr-nano-diarize..."
echo "  首次部署要下载 2.1 GB 模型，可能 5-30 分钟；服务重启后只需 26 秒-3 分钟。"
echo "  另开终端可看进度：tail -f $(pwd)/funasr_api.log"
echo "  ⚠️  不要中途 Ctrl-C 反复重试，模型支持断点续传，让它跑完更快。"
echo

S=$(date +%s)
CODE=$(curl -s -o /tmp/warmup_out.json -w '%{http_code}' --max-time 1800 \
  "$BASE/v1/audio/transcriptions" \
  -F file=@test.wav -F model=fun-asr-nano-diarize \
  -F response_format=diarized_json -F chunking_strategy=auto)
E=$(( $(date +%s) - S ))

if [ "$CODE" = "200" ]; then
  RSS=$(ps -o rss= -p "$(pgrep -f funasr_openai_server.py | head -1)" 2>/dev/null | tr -d ' ')
  GB=$(echo "${RSS:-0}" | awk '{printf "%.1f", $1/1048576}')
  echo "✅ 预热完成，耗时 ${E}s，常驻内存 ${GB} GB"
  echo "   下一步：./selftest.sh"
else
  echo "❌ 预热失败 HTTP $CODE，耗时 ${E}s"
  head -c 300 /tmp/warmup_out.json; echo
  echo "   排查：tail -30 funasr_api.log"
  exit 1
fi
