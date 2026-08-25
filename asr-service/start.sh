#!/bin/bash
# 启动服务（后台常驻，脱离终端）。可重复执行，已在跑则跳过。
cd "$(dirname "$0")"
PORT=${PORT:-8100}
HOST=${HOST:-127.0.0.1}   # 要给局域网用：HOST=0.0.0.0 ./start.sh
# 已知说话人（声纹）上限。Python 默认 4 是为了跟 OpenAI 官方接口对齐；
# 这里是本地专用部署，放到 16 以支持 5 人以上的会议。
MAX_KNOWN_SPEAKERS=${MAX_KNOWN_SPEAKERS:-16}

if pgrep -f "funasr_openai_server.py.*--port $PORT" >/dev/null; then
  echo "ℹ️  服务已在运行（PID $(pgrep -f "funasr_openai_server.py.*--port $PORT" | head -1)），跳过启动"
elif lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "❌ 端口 $PORT 被其他程序占用：" && lsof -nP -iTCP:"$PORT" -sTCP:LISTEN && exit 1
else
  [ -x .venv/bin/python ] || { echo "❌ 没有 .venv，先跑 ./install.sh"; exit 1; }
  nohup nice -n 10 .venv/bin/python funasr_openai_server.py \
    --host "$HOST" --port "$PORT" --speaker-db "" \
    --max-known-speakers "$MAX_KNOWN_SPEAKERS" \
    >> funasr_api.log 2>&1 & disown
  echo "已启动，等待 HTTP 就绪..."
fi

for i in $(seq 1 30); do
  if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$PORT/v1/models")" = "200" ]; then
    echo "✅ HTTP 就绪（${i}秒）  http://$HOST:$PORT/v1"
    echo "⚠️  模型是懒加载的，此刻还没进内存。首次转写前请跑 ./warmup.sh"
    exit 0
  fi
  sleep 1
done
echo "❌ 30 秒内未就绪，看日志：tail -30 funasr_api.log"; exit 1
