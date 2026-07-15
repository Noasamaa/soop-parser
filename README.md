# 直播解析（SOOP + YouTube · 自托管）

把 **SOOP** / **YouTube** 直播（及 YouTube 点播）解析成可播放地址，并在浏览器里用 **服务器出口 IP** 代理播放。  
部署在能正常访问目标站点的 VPS 上后，本地无需再开 VPN。

> **仅供个人学习与自用。** 内容版权归 SOOP / YouTube 与版权方。请勿公开分享代理链接，勿做成面向多人的转播站。

## 功能

| 平台 | 能力 |
|------|------|
| **SOOP** | 直播解析、多清晰度 HLS、密码房、可选账号登录 |
| **YouTube** | 直播 + 普通视频；HLS / 合并 MP4 代理播放（基于 [yt-dlp](https://github.com/yt-dlp/yt-dlp)） |

- Web 界面粘贴链接一键解析
- **代理播放**（默认开）：浏览器只连你的服务器
- 可选 `ACCESS_TOKEN` 防公网滥用
- Docker 一键部署

## 关于地区限制

| 误解 | 事实 |
|------|------|
| 解析出地址就能绕过墙 | CDN 仍可能校验请求源 IP |
| 正确做法 | 服务部署在**可访问**的地区，开启「代理播放」 |

YouTube 年龄限制 / 会员内容：配置 `YOUTUBE_COOKIES_FILE`（Netscape `cookies.txt`）。

## 快速开始

### 本地

```bash
cd soop-parser
python3 -m venv .venv   # 建议 Python 3.11+
source .venv/bin/activate
pip install -r requirements.txt
cp .env.example .env
uvicorn app.main:app --host 0.0.0.0 --port 8080
```

打开：http://127.0.0.1:8080

### Docker

```bash
cp .env.example .env
docker compose up -d --build
```

## 环境变量

| 变量 | 说明 |
|------|------|
| `ACCESS_TOKEN` | 访问口令；`X-Access-Token` 或 `?token=` |
| `SOOP_USERNAME` / `SOOP_PASSWORD` | 可选，SOOP 需登录场次 |
| `YOUTUBE_COOKIES_FILE` | 可选，YouTube cookies 路径（容器内路径） |
| `PUBLIC_BASE_URL` | **推荐**：公网域名，如 `https://xxxxxxxxx.xxx`。解析出的播放地址会带此前缀，供 poplayer / 外部播放器使用 |
| `PLAY_TOKEN_TTL` | 代理会话秒数，默认 600 |
| `HTTP_TIMEOUT` | 上游超时 |

### 播放地址为什么必须是你的域名？

对，**代理模式下**返回的应是：

```text
https://你的域名/api/hls/{token}/playlist.m3u8
```

而不是 `googlevideo.com` / SOOP CDN 直链。

- 浏览器 / **poplayer** 只访问你的服务器  
- 服务器再去拉 YouTube / SOOP（用服务器出口 IP）  
- m3u8 里的分片也会被改写成 `https://你的域名/api/hls/.../proxy?u=...`  

在 Nginx/Caddy 反代时请设置 `PUBLIC_BASE_URL=https://你的域名`，或正确传 `X-Forwarded-Proto` / `X-Forwarded-Host`。

## API

### `POST /api/resolve`

自动识别平台：

```json
{
  "url": "https://www.youtube.com/watch?v=xxxx",
  "stream_password": "",
  "proxy": true
}
```

或 SOOP：

```json
{
  "url": "https://play.sooplive.com/loltw/295590617",
  "proxy": true
}
```

返回字段包含 `platform`、`is_live`、`qualities[]`：

- `protocol: "hls"` → `play_url` 为 `/api/hls/{token}/playlist.m3u8`
- `protocol: "progressive"` → `play_url` 为 `/api/media/{token}`

### 其他

- `GET /api/hls/{token}/playlist.m3u8` — HLS 代理
- `GET /api/media/{token}` — 渐进式媒体代理（支持 Range）
- `GET /health` — 健康检查

## 测试

```bash
pytest -q
```

## 技术说明

### SOOP

对齐 [Streamlink SOOP 插件](https://github.com/streamlink/streamlink)：

1. `player_live_api.php`（`live` / `aid`）
2. `broad_stream_assign.html` → `view_url`
3. HLS + `aid`；代理改写 m3u8

### YouTube

使用 **[yt-dlp](https://github.com/yt-dlp/yt-dlp)**（依赖要求 `>=2026.7.4`，即 2026.07.04 这一代）提取可播放格式：

- 优先 **HLS**（直播常见 144p–720p/1080p 合并流，适合代理）
- 其次 **progressive MP4**（点播常见最高约 360p 合并流）
- 跳过仅视频/仅音频的分离 DASH（浏览器路径不做 ffmpeg 混流；高清点播因此可能只有 360p）
- 需要 **Node.js** 运行时（Docker 镜像已含）；首次解析会拉取 yt-dlp EJS 组件

## 免责声明

不破解 DRM，不提供盗版片源；仅转发服务器本就可访问的公开流。请自行遵守当地法律与平台服务条款。
