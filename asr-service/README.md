# 本地语音转写服务

OpenAI `gpt-4o-transcribe-diarize` 协议兼容的本地转写服务，带**说话人分离**和**声纹识人**。
纯 CPU 推理，数据不出内网。

引擎是 **Fun-ASR-Nano-2512**（800M）。服务端保证**默认只跑这一个引擎**：漏传 `model`
或沿用 OpenAI 官方模型名，都会落到 Nano，不会意外加载别的模型。

这个目录是自包含的，可以整个拷进任何项目当作语音识别模块。

---

## 快速开始

```bash
./install.sh     # 环境检查 + 装依赖（首次 3-10 分钟）
./start.sh       # 启动，8 秒就绪
./warmup.sh      # 预热，触发模型加载（首次要下 2.1GB，5-30 分钟）
./selftest.sh    # 验收，6 项全绿才算装好
```

四条命令跑完即可交付客户端。把 [`CLIENT.md`](CLIENT.md) 发给客户端开发者就行。

> **`warmup.sh` 不能跳过。** 模型是懒加载的——`start.sh` 返回就绪只代表 HTTP 起来了，
> 内存里还没有模型。不预热的话，客户端第一个请求会干等几分钟，看起来像故障。

---

## 目录内容

| 文件 | 说明 |
|---|---|
| `README.md` | 本文档，部署和运维 |
| [`CLIENT.md`](CLIENT.md) | **给客户端开发者的接口文档**，可直接转发 |
| `funasr_openai_server.py` | 服务主程序（单文件，765 行） |
| `install.sh` | 前置检查 + 虚拟环境 + 依赖 |
| `start.sh` / `stop.sh` | 启动 / 停止 |
| `warmup.sh` | 预热，触发模型下载与加载 |
| `selftest.sh` | 验收自检，6 项检查 |
| `make_reference.py` | 制作声纹参考片段，输出 data URL |

运行后会多出 `.venv/`（依赖）、`funasr_api.log`（日志）、`test.wav`（预热用静音文件）。
模型缓存在 `~/.cache/modelscope`（2.1 GB），不在本目录内。

---

## 硬件要求

| 项 | 要求 |
|---|---|
| 系统 | macOS / Linux（Windows 用 WSL2） |
| Python | 3.10 – 3.12 |
| 内存 | ≥ 8 GB |
| 磁盘 | ≥ 5 GB |
| ffmpeg | 必装 |
| GPU | **不需要**，代码写死走 CPU |

内存实测（Apple M4 Pro，仅 Nano 单引擎）：

| 状态 | 常驻内存 |
|---|---|
| 已启动、未加载模型 | 0.56 GB |
| 预热后（模型常驻不释放） | **2.7 GB** |
| 转写过若干次后（含推理缓冲） | **3.5 – 4.0 GB** |

按此选机器：

- **16 GB 以上 Apple Silicon** — 舒服，还有余量
- **8 GB Apple Silicon** — 可跑，但只能跑这一个引擎，且别同时开大型应用。内存吃紧时模型加载会从 26 秒劣化到 3 分钟（swap 颠簸）
- **Intel Mac** — 能跑，慢 2-3 倍。离线转写没问题，实时分块很紧张
- **低于 8 GB** — 不要装，进程会被 OOM 杀掉

性能：RTF ≈ 0.25，即 30 秒音频约 7.5 秒转完。推理串行排队，**一台机器支撑 3-4 个并发会议客户端**。

---

## 详细步骤

### 1. 安装

```bash
./install.sh
```

做三件事：检查 ffmpeg / Python 版本 / 内存 / 磁盘 → 建 `.venv` → 装依赖。
可重复执行，`.venv` 已存在会跳过。

**失败处理**：
- 缺 ffmpeg → macOS `brew install ffmpeg`，Debian `sudo apt install -y ffmpeg`
- Python 版本不对 → 装 3.11。3.13+ 有依赖兼容问题
- `torch==2.13.0` 找不到 → 编辑 `install.sh` 去掉 `torch` 的版本号钉版

### 2. 启动

```bash
./start.sh                  # 仅本机访问
HOST=0.0.0.0 ./start.sh     # 局域网其他机器也能访问，见下方"局域网"
PORT=8200 ./start.sh        # 换端口
```

用 `nohup ... & disown` 启动，**脱离终端**，关掉终端窗口也不会被杀。
但**扛不住重启**，需要开机自启见"常驻运行"。

**失败处理**：`tail -30 funasr_api.log`。端口被占时脚本会打印占用它的进程。

### 3. 预热

```bash
./warmup.sh
```

| 场景 | 耗时 |
|---|---|
| 全新机器首次 | **5-30 分钟**（下载 2.1 GB 模型） |
| 服务重启后首次 | 26 秒 – 3 分钟（只是加载进内存） |
| 热态 | 30 秒音频约 7.5 秒 |

另开终端 `tail -f funasr_api.log` 可看下载进度。**不要中途反复 Ctrl-C**，
模型支持断点续传，让它跑完比重开快。

### 4. 验收

```bash
./selftest.sh
```

7 项检查：健康检查、模型注册、两项参数校验、转写返回结构、model 回显、默认路由。
**必须 `通过 7 项，失败 0 项`** 才能交付。

最后的内存读数是信息项不参与判定：

| 读数 | 含义 |
|---|---|
| 2.5 – 4.0 GB | 正常 |
| < 1 GB | 进程闲置被换出 swap，正常，下次请求换回 |
| > 4 GB | 有请求点名过 `funasr-paraformer-diarize`，两个引擎都驻留了 |

---

## 常驻运行

`start.sh` 扛不住重启。要开机自启 + 崩溃自动拉起：

### macOS

```bash
D=$(pwd)
cat > ~/Library/LaunchAgents/com.asr.transcribe.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.asr.transcribe</string>
  <key>ProgramArguments</key><array>
    <string>$D/.venv/bin/python</string>
    <string>$D/funasr_openai_server.py</string>
    <string>--host</string><string>127.0.0.1</string>
    <string>--port</string><string>8100</string>
    <string>--speaker-db</string><string></string>
  </array>
  <key>WorkingDirectory</key><string>$D</string>
  <key>StandardOutPath</key><string>$D/funasr_api.log</string>
  <key>StandardErrorPath</key><string>$D/funasr_api.log</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>Nice</key><integer>10</integer>
</dict></plist>
EOF
./stop.sh
launchctl load ~/Library/LaunchAgents/com.asr.transcribe.plist
```

### Linux

```bash
D=$(pwd)
sudo tee /etc/systemd/system/asr-transcribe.service > /dev/null <<EOF
[Unit]
Description=Local OpenAI-compatible transcription service (Fun-ASR-Nano)
After=network.target
[Service]
User=$USER
WorkingDirectory=$D
ExecStart=$D/.venv/bin/python funasr_openai_server.py --host 127.0.0.1 --port 8100 --speaker-db ""
Restart=always
Nice=10
[Install]
WantedBy=multi-user.target
EOF
./stop.sh
sudo systemctl daemon-reload && sudo systemctl enable --now asr-transcribe
```

⚠️ 服务每次重启后模型都会卸载，**下一个请求要重新加载 26 秒-3 分钟**。
建议把 `warmup.sh` 也加进开机脚本。

---

## 局域网访问

```bash
HOST=0.0.0.0 ./start.sh
ipconfig getifaddr en0     # macOS 查本机 IP；Linux 用 hostname -I
```

放行防火墙 8100 端口（macOS 首次会弹窗点允许；Linux `sudo ufw allow 8100`），
客户端 Base URL 改成 `http://<本机IP>:8100/v1`。

⚠️ **服务无鉴权**，`Authorization` 头收下但不校验。**只能用于可信内网**。
需要更大范围暴露时，前面加 Nginx/Caddy 做 token 校验 + HTTPS，不要裸奔公网。

---

## 启动参数

`start.sh` 里用的是推荐配置。要调整可直接改脚本或手工启动：

```
--host        默认 127.0.0.1；局域网服务改 0.0.0.0
--port        默认 8100
--speaker-db  服务端声纹库 JSON 路径；传 "" 关闭（推荐，见禁止事项第 4 条）
--sv-threshold        声纹匹配余弦阈值，默认 0.3
--speech-noise-thres  VAD 语音/噪声阈值，默认 0.2。FunASR 原厂默认对会议远场收音过于
              激进，实测会静默丢掉约 25% 的语音；调低可保住这些语音，噪声被误识别成
              文字时向 0.6 调高
--max-end-silence-time  VAD 断句静音时长(ms)，默认 1200（原厂 800）
--no-refine-nano      关闭 Nano 的标点级说话人重判。默认开启：Nano 句子偏长，一个 VAD
              段里可能含两个人的话，重判会在标点处切开并用 CAM++ 重新聚类，代价 +6% 耗时
--max-known-speakers  每请求 known_speaker_* 上限，默认 4（镜像官方限制）
--moss-url    MOSS 后端地址，默认 http://127.0.0.1:7860（默认不启用）
```

---

## 故障速查

| 现象 | 原因 | 处理 |
|---|---|---|
| `connection refused` | 服务没起来 | `tail -30 funasr_api.log`；`./start.sh` |
| 首次请求很久不返回 | 正在下载 2.1 GB 模型 | 正常，等待，日志看进度 |
| 重启后第一次请求慢 26 秒-3 分钟 | 懒加载 | 正常，跑 `./warmup.sh` 可避免用户撞上 |
| 常驻内存 2.7-4.0 GB 不降 | 模型加载后常驻 | 正常，`./stop.sh` 才释放 |
| 常驻内存到 4.3 GB | 有请求显式点名了 `funasr-paraformer-diarize`，两个引擎同时驻留 | 正常现象。只用 Nano 的话 `./stop.sh && ./start.sh && ./warmup.sh` 可释放 |
| 400 `truncated data URL` | curl 用 `-F` 传了声纹 | 改用 `--form-string`，见 `CLIENT.md` |
| 400 其他 | 参数问题 | 看响应 `detail`：缺 file / 声纹数量不匹配 / 超 4 人 / 非法 base64 |
| 502 | 请求了 `moss-transcribe-diarize` 但没部署 MOSS | 本服务不含 MOSS，改用 `fun-asr-nano-diarize` |
| 整句语音丢失没被转写 | VAD 阈值偏高 | 启动加 `--speech-noise-thres 0.15` |
| 一句话混进两个人的发言 | 说话人切分粒度 | 确认没加 `--no-refine-nano`。仍有问题属已知限制，切换点精度约 ±1 秒 |
| 进程被 `Killed` | 内存不足 | 该机器配置不够 |

---

## 禁止事项

1. **不要**用 `model=funasr-paraformer-diarize` —— 那会额外加载 1.6 GB 的 Paraformer 且质量更差。
   本服务只打算跑 Nano；漏传 `model` 或用 `gpt-4o-transcribe-diarize` 都会安全地落到 Nano，不必担心。
2. **不要**用 curl 的 `-F` 传 `known_speaker_references[]` —— 用 `--form-string`，原因见 `CLIENT.md`。
3. **不要**跳过 `./warmup.sh` 就交付 —— 客户端第一个请求会等几分钟，看起来像故障。
4. **不要**去掉 `--speaker-db ""` —— 会启用服务端私有声纹库扩展，导致客户端无法再切换到 OpenAI 官方 API。
5. **不要**在同一台机器起多个端口实例 —— 共享 CPU 且各占 2.7 GB 内存，没有收益。
6. **不要**把 `--host 0.0.0.0` 的服务暴露到公网 —— 无鉴权。

---

## 卸载

```bash
./stop.sh
launchctl unload ~/Library/LaunchAgents/com.asr.transcribe.plist 2>/dev/null   # macOS
sudo systemctl disable --now asr-transcribe 2>/dev/null                        # Linux
rm -rf .venv funasr_api.log test.wav ~/.cache/modelscope
```
