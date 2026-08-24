# Meeting Room

Meeting Room:基于 Meetily 改造的独立 web 服务端:面向**本地自部署**的会议音频转录(带说话人分离)+ 纪要服务。数据全部留在你自己的机器上,不经过任何第三方。技术栈:Go 1.25 + Gorilla mux/websocket + sqlx + SQLite,编译产物是**单个二进制**,不依赖数据库服务或容器。

桌面版(Tauri)的编排知识在这里以服务形态复刻:上传 → 本地任务队列 → ffmpeg 归一化 → **10 分钟大段串行**提交 OpenAI 兼容转录服务(diarized_json)→ 段级说话人/时间戳入库 → WebSocket 进度推送。

## 架构

```
浏览器 (React + Vite,由服务端托管)
   │ REST (mux)               │ WebSocket (gorilla/websocket)
   ▼                          ▼
┌─────────────────────────────────────────────┐
│ meeting-room (Go 单二进制)                  │
│  ├─ internal/api        REST + WS 入口      │
│  ├─ internal/ws         事件广播 Hub        │
│  ├─ internal/jobs       转录 worker         │
│  ├─ internal/transcribe OpenAI 兼容客户端    │
│  ├─ internal/audio      ffmpeg 归一化       │
│  └─ internal/store      sqlx + migrations   │
└──────┬──────────┬───────────┬──────────────┘
       ▼          ▼           ▼
  SQLite 单文件   ./data 目录   转录服务(本地或局域网)
```

设计要点(从桌面版实测得出,勿改):

- **大段串行提交**(默认 600s):MOSS 类服务单推理锁,小段提交 RTF 恶化 4~8 倍;说话人编号只在单请求内一致。
- 动态超时 `120s + 音频秒数 × 6`。
- 时间戳重定基:服务返回段内相对时间,worker 加 chunk 偏移得到全程绝对时间。
- 单 worker 串行消费任务:GPU 端只有一个锁,服务端并发只会排队。

## 快速开始

只需要 Go 和 ffmpeg,数据库随程序自带(SQLite 单文件,首次运行自动创建):

```bash
go build -o meeting-room ./cmd/server
./meeting-room                      # 打开 http://localhost:8090
```

数据库和音频都在 `./data/` 下,备份就是复制这个目录。转录服务地址、纪要 LLM、
日历订阅等都在页面的设置里配,不用改配置文件。

想跑在容器里也可以:`cp .env.example .env && docker compose up --build`。

冒烟测试:

```bash
TOKEN=change-me
# 上传音频 → 返回 meeting + job
curl -H "Authorization: Bearer $TOKEN" \
  -F file=@meeting.wav -F title="周会" -F language=zh \
  http://localhost:8090/api/v1/meetings/import

# 进度(也可 websocket 连 /api/v1/ws?token=$TOKEN)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8090/api/v1/meetings/<id>

# 转录结果(带 speaker / startMs / endMs)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8090/api/v1/meetings/<id>/transcript
```

## API

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/v1/meetings/import` | multipart 上传(file, title?, language?),返回 202 + job |
| GET | `/api/v1/meetings` | 会议列表 |
| GET | `/api/v1/meetings/{id}` | 会议详情 + 最近 job 状态 |
| GET | `/api/v1/meetings/{id}/transcript` | 转录段(speaker/startMs/endMs/text) |
| POST | `/api/v1/meetings/{id}/retranscribe` | 重转录 |
| DELETE | `/api/v1/meetings/{id}` | 删除(含音频文件) |
| GET | `/api/v1/settings` | 当前生效配置(transcribe + summary) |
| PUT | `/api/v1/settings/transcribe` | 保存转录服务配置 `{baseUrl, model, apiKey}` |
| POST | `/api/v1/settings/transcribe/test` | 测试连通性(body 传配置=保存前验证;空 body=测当前配置) |
| PUT | `/api/v1/settings/summary` | 保存总结 LLM 配置 `{baseUrl, model, apiKey}` |
| GET | `/api/v1/ws` | WebSocket,推送 `job-progress` 事件 |

**配置模型**:转录/总结服务的地址、模型、key 都在**界面(设置 API)里配,存数据库**——本地 MOSS、局域网部署、线上 OpenAI(`https://api.openai.com/v1`)可随时切换,不用重启。`.env` 里的 `TRANSCRIBE_*` 只是部署级默认值,界面留空的字段才会用到。

**认证**:`MEETING_ROOM_API_TOKEN` 为空(默认)= 不鉴权,本地随便用;部署给团队时设一个 token,请求带 `Authorization: Bearer <token>`(WS 用 `?token=`)。

## Roadmap

- [ ] **前端**:Next.js UI 移植——写 transport 适配层(`invoke()` → fetch,`listen()` → WS),复用桌面版组件
- [ ] **浏览器实时录音**:getUserMedia + getDisplayMedia(标签页音频)→ WS 上行;边录边传 + 滚动粗稿(每 5-10 分钟一大段)+ 会后全量终稿(说话人全程一致)
- [ ] **总结**:LLM 纪要生成(Ollama / OpenAI 兼容接口),复用桌面版 prompt 编排
- [ ] **多用户**:users/teams 表 + 会话认证,替换共享 token
- [ ] **检索**:转录全文搜索(SQLite FTS5,自带中文 unicode61 分词)
- [ ] 存储保留策略(音频文件生命周期)、单文件时长限额、上传大小限制

## 致谢

产品形态与音频编排思路来自 [Meetily](https://github.com/Zackriya-Solutions/meetily)(MIT)的桌面版。
本项目是独立实现的 Go 服务端 + Web 前端,不含上游代码。

## 许可证

[MIT](LICENSE)
