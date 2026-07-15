# 直播解析（SOOP + YouTube · Go）

自托管直播/点播解析 + **HLS 反向代理**。浏览器与 PotPlayer 只访问你的域名，由服务器出口 IP 拉 SOOP / YouTube。

> 仅供个人自用。勿公开分享代理链接。

## 为什么是 Go

| | Python (旧) | Go (现) |
|--|-------------|--------|
| 常驻内存 | 较高（解释器 + uvicorn） | 更低（静态二进制） |
| 分片代理 | 可用 | 原生并发/流式拷贝更省 |
| YouTube | yt-dlp 库 | **yt-dlp CLI**（同样紧跟官方） |

## 功能

- SOOP 直播多清晰度 HLS
- YouTube 直播 / 部分点播（HLS 或 progressive）
- 代理播放（绝对 URL，便于 PotPlayer）
- HTTP HEAD 兼容（不拉完整分片）
- 会话上限 + 滑动 TTL
- 上游 host 白名单（收紧，防开放代理）

## 快速开始

### 本地

```bash
# 需要 Go 1.22+；YouTube 需要 yt-dlp + node
go build -o soop-parser ./cmd/soop-parser
export PUBLIC_BASE_URL=http://127.0.0.1:8080
./soop-parser
```

### Docker

```bash
cp .env.example .env
# PUBLIC_BASE_URL=https://soop.uuun.de
docker compose up -d --build
```

## 环境变量

| 变量 | 说明 |
|------|------|
| `PUBLIC_BASE_URL` | 公网域名，如 `https://soop.uuun.de` |
| `ACCESS_TOKEN` | 可选访问令牌；留空则不鉴权 |
| `PLAY_TOKEN_TTL` | 播放会话秒数（滑动续期） |
| `MAX_SESSIONS` | 最大会话数，默认 64 |
| `SOOP_USERNAME` / `SOOP_PASSWORD` | 可选 |
| `YOUTUBE_COOKIES_FILE` | 可选 |

## API

- `POST /api/resolve` `{"url":"...","proxy":true}`
- `GET /api/hls/{token}/playlist.m3u8`
- `GET /api/hls/{token}/proxy?u=...`（仅白名单 host）
- `GET /api/media/{token}`
- `GET /health`

## 测试

```bash
go test ./...
```
