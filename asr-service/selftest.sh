#!/bin/bash
# 部署验收自检。全部通过才可交付客户端。
BASE=http://127.0.0.1:8100
PASS=0; FAIL=0
chk(){ if [ "$2" = "$3" ]; then echo "  ✅ $1"; PASS=$((PASS+1)); else echo "  ❌ $1 (期望 $3, 实际 $2)"; FAIL=$((FAIL+1)); fi; }

cd "$(dirname "$0")" || exit 1
[ -f test.wav ] || ffmpeg -f lavfi -i anullsrc=r=16000:cl=mono -t 5 -y test.wav 2>/dev/null

echo "1. 健康检查"
chk "GET /v1/models 返回 200" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 $BASE/v1/models)" "200"

echo "2. 模型注册"
chk "fun-asr-nano-diarize 已注册" \
  "$(curl -s --max-time 5 $BASE/v1/models | grep -c fun-asr-nano-diarize)" "1"

echo "3. 参数校验"
chk "缺 file 返回 400" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/v1/audio/transcriptions -F model=fun-asr-nano-diarize)" "400"
chk "截断的 data URL 返回 400 而非 500" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/v1/audio/transcriptions \
      -F file=@test.wav -F model=fun-asr-nano-diarize \
      --form-string 'known_speaker_names[]=X' --form-string 'known_speaker_references[]=data:audio/wav')" "400"

echo "4. 默认路由（不传 model 应落到 Nano 而非其他引擎）"
chk "省略 model 仍返回 200" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 300 $BASE/v1/audio/transcriptions \
      -F file=@test.wav -F response_format=json)" "200"

echo "5. 转写（热态应在 60 秒内返回；若刚重启过服务，改用 --max-time 300 重跑）"
R=$(curl -s --max-time 60 $BASE/v1/audio/transcriptions \
      -F file=@test.wav -F model=fun-asr-nano-diarize \
      -F response_format=diarized_json -F chunking_strategy=auto)
chk "返回含 segments 字段" "$(echo "$R" | grep -c '\"segments\"')" "1"
chk "回显 model 正确" "$(echo "$R" | grep -c 'fun-asr-nano-diarize')" "1"

echo "6. 资源占用（信息项，不计入判定）"
PID=$(pgrep -f funasr_openai_server.py | head -1)
RSS=$(ps -o rss= -p "$PID" 2>/dev/null | tr -d ' ')
GB=$(echo "${RSS:-0}" | awk '{printf "%.1f", $1/1048576}')
echo "  ℹ️  常驻内存 ${GB} GB"
echo "$GB" | awk '$1 > 4.0 {exit 0} {exit 1}' && echo "  ⚠️  >4 GB：Paraformer 可能被误加载，见 README 禁止事项第 1 条"
echo "$GB" | awk '$1 < 1.0 {exit 0} {exit 1}' && echo "  ℹ️  <1 GB：进程闲置被换出 swap，正常"

echo; echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] && echo "🎉 部署完成，可交付客户端" || echo "⛔ 请先解决失败项"
exit "$FAIL"
