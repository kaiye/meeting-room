# 转写服务 — 客户端接口文档

本服务以 **OpenAI `gpt-4o-transcribe-diarize` 协议**提供带说话人分离和声纹识人的语音转写。
可直接用 OpenAI SDK，**只改 Base URL 即可在本服务与 OpenAI 官方之间切换**。

## 连接信息

| 配置项 | 值 |
|---|---|
| Base URL | `http://127.0.0.1:8100/v1`（局域网换成 `http://<服务器IP>:8100/v1`） |
| 转写接口 | `POST /v1/audio/transcriptions`（multipart/form-data） |
| 健康检查 | `GET /v1/models`，返回 200 即 HTTP 就绪 |
| 鉴权 | `Authorization: Bearer <任意字符串>`（本地不校验，但建议带上以便切官方） |
| **模型名** | **`fun-asr-nano-diarize`（必传）** |

> **关于 `model`：** 本服务的默认引擎就是 Nano——漏传 `model`，或沿用官方名
> `gpt-4o-transcribe-diarize`，都会落到 Nano，不会出错。但仍**建议显式传
> `fun-asr-nano-diarize`**，让请求自解释、日志可追溯。
>
> 唯一要避开的是 `funasr-paraformer-diarize`：那是另一个更快但更不准的引擎，
> 会让服务端额外加载 1.6 GB 模型。除非服务端运维明确要求，否则不要用。
>
> 建议把 Base URL 和 model 做成**联动的一对配置**：
>
> | 指向 | Base URL | model |
> |---|---|---|
> | 本地服务 | `http://<IP>:8100/v1` | `fun-asr-nano-diarize` |
> | OpenAI 官方 | `https://api.openai.com/v1` | `gpt-4o-transcribe-diarize` |

## 请求参数

| 字段 | 必填 | 说明 |
|---|---|---|
| `file` | ✅ | 音频文件。**任意 ffmpeg 可读格式**（wav/mp3/m4a/ogg…），服务端自动转 16k 单声道，**客户端不需要转格式**。≤25 MB |
| `model` | 建议 | 固定 `fun-asr-nano-diarize`。省略也能工作（服务端默认就是 Nano），但显式传更清晰 |
| `response_format` | 建议 | `diarized_json`（带说话人）。另支持 `json` / `text` / `verbose_json` |
| `chunking_strategy` | ✅ | 固定 `auto`。OpenAI 官方对 >30s 音频强制要求，本地接受并忽略 |
| `known_speaker_names[]` | 可选 | 说话人真名，可重复传多次，**≤4 人** |
| `known_speaker_references[]` | 可选 | 与 names 一一对应的参考音频，格式 `data:audio/wav;base64,<...>`，**每段 2–10 秒** |
| `stream` | 可选 | `true` 时以 SSE 推送 |
| `language` | 可选 | 如 `zh`。接受并忽略（模型自动中英） |

**不要传 `prompt`**：OpenAI 官方 diarize 模型不支持这个字段，传了会报错。

## 响应结构

`response_format=diarized_json` 的实测输出：

```json
{
  "task": "transcribe",
  "duration": 30.0,
  "text": "训练了一个AI，然后是AI再去帮你去做这个这个技术，……",
  "segments": [
    { "id": "seg_0001", "type": "transcript.text.segment",
      "start": 0.0, "end": 1.19, "speaker": "A", "text": "训练了一个AI，" },
    { "id": "seg_0002", "type": "transcript.text.segment",
      "start": 1.19, "end": 3.87, "speaker": "A", "text": "然后是AI再去帮你去做这个这个技术，" }
  ],
  "usage": { "type": "duration", "seconds": 30.0 },
  "model": "fun-asr-nano-diarize"
}
```

关于 `speaker` 字段：

- **未传声纹**时是 `A` / `B` / `C`…，**仅在本次请求内有效**。
  分块调用时，同一个人在不同块里可能拿到不同字母，**不要跨块直接拼**。
- **传了声纹**时直接是传入的真名，跨块稳定。这是分块场景推荐传声纹的主要原因。

**只应依赖** `segments[].start` / `end` / `speaker` / `text` 和顶层 `text`。
`usage` 的结构本地和官方不同，不要依赖。

## 声纹识人

给每位说话人准备一段 2–10 秒的清晰独白，转成 base64 data URL 传入。

> **实测：2–3 秒效果最好，更长反而下降。** 长片段会累积共同的房间混响和信道特征，
> 把**不同人**之间的相似度也一起抬高，反而降低区分度。

服务端提供了制作工具（只依赖 ffmpeg 和标准库）：

```bash
python3 make_reference.py 会议录音.wav 12.6 > 李工.txt      # 从第 12.6 秒裁 3 秒
python3 make_reference.py 会议录音.wav 12.6 2.5 > 李工.txt  # 指定时长
```

## 调用示例

### curl

```bash
# 基础调用
curl http://127.0.0.1:8100/v1/audio/transcriptions \
  -H "Authorization: Bearer sk-local" \
  -F file=@chunk.wav \
  -F model=fun-asr-nano-diarize \
  -F response_format=diarized_json \
  -F chunking_strategy=auto

# 带声纹识人
REF=$(python3 make_reference.py 李工样本.wav 0)
curl http://127.0.0.1:8100/v1/audio/transcriptions \
  -F file=@chunk.wav \
  -F model=fun-asr-nano-diarize \
  -F response_format=diarized_json \
  -F chunking_strategy=auto \
  --form-string "known_speaker_names[]=李工" \
  --form-string "known_speaker_references[]=$REF"
```

> ⚠️ **声纹字段必须用 `--form-string`，不能用 `-F`。**
> `-F` 会把 `data:audio/wav;base64,...` 里的分号当成 content-type 参数分隔符，
> 把值截断成 `data:audio/wav`，服务端返回 400。
> **这个坑只有 curl 有**，用 Python requests / OpenAI SDK 不受影响。

### Python（requests）

```python
import base64, requests

BASE  = "http://127.0.0.1:8100/v1"
MODEL = "fun-asr-nano-diarize"        # 指向官方时换成 gpt-4o-transcribe-diarize

def make_ref(path: str) -> str:
    return "data:audio/wav;base64," + base64.b64encode(open(path, "rb").read()).decode()

SPEAKERS = {"李工": make_ref("li.wav"), "王工": make_ref("wang.wav")}   # 每段 2-3 秒

def transcribe(chunk_path: str, offset: float) -> list[dict]:
    r = requests.post(
        f"{BASE}/audio/transcriptions",
        files={"file": open(chunk_path, "rb")},
        data={
            "model": MODEL,
            "response_format": "diarized_json",
            "chunking_strategy": "auto",
            "known_speaker_names[]": list(SPEAKERS),
            "known_speaker_references[]": list(SPEAKERS.values()),
        },
        timeout=60,
    )
    r.raise_for_status()
    return [                                      # 加上本块的全局偏移
        {**s, "start": s["start"] + offset, "end": s["end"] + offset}
        for s in r.json()["segments"]
    ]
```

### OpenAI SDK

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:8100/v1", api_key="sk-local")

resp = client.audio.transcriptions.create(
    model="fun-asr-nano-diarize",
    file=open("chunk.wav", "rb"),
    response_format="diarized_json",
    chunking_strategy="auto",
    extra_body={
        "known_speaker_names[]": ["李工"],
        "known_speaker_references[]": [make_ref("li.wav")],
    },
)
```

## 性能与超时

Apple M4 Pro 实测：

| 项 | 值 |
|---|---|
| 转写速度 | RTF 0.25，30 秒音频约 **7.5 秒**返回 |
| 并发能力 | 推理串行排队（不会失败，只会等），**一台机器支撑 3–4 个并发会议客户端** |
| **服务重启后首个请求** | 需额外 **26 秒 – 3 分钟**加载模型，超时设 **300 秒** |
| 全新部署的首个请求 | 还要下载 2.1 GB 模型，超时设 **1800 秒** |
| 常规请求 | 60 秒足够 |

配置较低的机器（Intel Mac）RTF 可能到 0.5–0.7，需相应放大块长或降低并发。

## 准实时分块规范

会议进行中每隔一段时间发一块，获得准实时体验。

| 项 | 规范 |
|---|---|
| 块长 | **30–60 秒**。太短（<15s）声纹和聚类不稳，太长延迟高 |
| 切分点 | **在静音处下刀**，不要按死时间硬切。到达目标块长后找最近的 ≥300ms 静音处截断；连续讲话找不到静音时，最多延到上限后硬切 |
| 编码 | 直接传录音原格式，服务端会转码。想省带宽可自行转 16k 单声道 |
| 时间戳 | 全局时间 = `本块起始偏移 + segments[].start`，偏移由客户端记账 |
| 声纹 | **每块都重传**同样的 `known_speaker_*`。服务端按内容哈希缓存了声纹向量，重复传不产生额外开销，但能保证跨块说话人标签一致 |
| 合并 | 按全局时间追加各块 segments；相邻的同名 speaker 段可合并显示 |
| 失败重试 | 单块失败重试 2 次；仍失败则跳过该块并在 UI 标注空洞，**不要阻塞后续块** |

体验延迟 ≈ 块长 + 推理时间。30 秒块约 37 秒，45 秒块约 56 秒。

## 错误码

| 状态码 | 含义 | 处理 |
|---|---|---|
| 400 | 参数问题 | 看响应 `detail`：缺 `file`、声纹 names/references 数量不匹配、超过 4 人、非法 base64、data URL 被截断 |
| 404 | 路径错了 | 转写是 `/v1/audio/transcriptions`，健康检查是 `/v1/models` |
| 502 | 请求了 `moss-transcribe-diarize` 但服务端没部署 MOSS | 改用 `fun-asr-nano-diarize` |
| 超时无响应 | 服务重启后模型正在加载 | 放大超时到 300 秒，见"性能与超时" |
| connection refused | 服务没运行 | 联系服务端运维 |

## 切换到 OpenAI 官方的检查清单

- [ ] Base URL 和 `model` 是联动配置，不是硬编码
- [ ] 只用了标准字段：`file` / `model` / `response_format` / `chunking_strategy` / `stream` / `known_speaker_*` / `language`
- [ ] 没有传 `prompt`（官方 diarize 不支持）
- [ ] 参考音频 **2–10 秒**、**≤4 人**（官方硬校验）
- [ ] 单块文件 **≤25 MB**（官方硬限制）
- [ ] 没有依赖 `usage` 的结构
- [ ] `Authorization` 头带的是真实 API Key

两端已知差异（不影响按本文档实现的客户端）：

| | 本地服务 | OpenAI 官方 |
|---|---|---|
| `usage` 结构 | `{"type":"duration","seconds":N}` | token 计价结构 |
| 语言 | 中英 | 多语种 |
| 数据 | 不出内网 | 上传云端 |
| 费用 | 免费 | 按量计费 |
| 声纹阈值/聚类实现 | CAM++ | 未公开 |
