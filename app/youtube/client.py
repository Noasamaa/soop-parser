from __future__ import annotations

import asyncio
import logging
import re
from typing import Any, Dict, List, Optional
from urllib.parse import parse_qs, urlparse

from app.soop.models import (
    GeoRestrictedError,
    LoginRequiredError,
    NotLiveError,
    QualityStream,
    ResolveFailedError,
    ResolveResult,
    SoopError,
)

log = logging.getLogger(__name__)

YOUTUBE_HOST_RE = re.compile(
    r"(^|\.)((youtube\.com)|(youtube-nocookie\.com)|(youtu\.be)|(music\.youtube\.com))$",
    re.IGNORECASE,
)

# Prefer browser-playable combined streams over video-only DASH.
HLS_PROTOCOLS = {"m3u8", "m3u8_native", "http_dash_segments"}  # we mainly use m3u8*


def is_youtube_url(url: str) -> bool:
    try:
        host = (urlparse(url.strip()).hostname or "").lower()
    except Exception:  # noqa: BLE001
        return False
    if not host:
        return False
    if host == "youtu.be" or host.endswith(".youtu.be"):
        return True
    return bool(YOUTUBE_HOST_RE.search(host))


def _video_id_hint(url: str) -> str:
    u = url.strip()
    parsed = urlparse(u)
    host = (parsed.hostname or "").lower()
    if host == "youtu.be":
        return parsed.path.lstrip("/").split("/")[0] or "youtube"
    qs = parse_qs(parsed.query)
    if "v" in qs and qs["v"]:
        return qs["v"][0]
    parts = [p for p in parsed.path.split("/") if p]
    if parts and parts[0] in {"live", "watch", "embed", "shorts", "v"} and len(parts) > 1:
        return parts[1]
    if parts:
        return parts[-1]
    return "youtube"


def _is_hls_format(fmt: Dict[str, Any]) -> bool:
    proto = (fmt.get("protocol") or "").lower()
    url = fmt.get("url") or ""
    if "m3u8" in proto:
        return True
    if ".m3u8" in url:
        return True
    return False


def _has_video(fmt: Dict[str, Any]) -> bool:
    vcodec = fmt.get("vcodec")
    return bool(vcodec) and vcodec != "none"


def _has_audio(fmt: Dict[str, Any]) -> bool:
    acodec = fmt.get("acodec")
    return bool(acodec) and acodec != "none"


def _height(fmt: Dict[str, Any]) -> int:
    h = fmt.get("height")
    if isinstance(h, int) and h > 0:
        return h
    # Fallback parse from format_note / format_id
    note = f"{fmt.get('format_note') or ''} {fmt.get('format_id') or ''}"
    m = re.search(r"(\d{3,4})p", note)
    if m:
        return int(m.group(1))
    return 0


def _label_for(fmt: Dict[str, Any], *, protocol: str) -> str:
    h = _height(fmt)
    fps = fmt.get("fps")
    tbr = fmt.get("tbr")
    bits: List[str] = []
    if h:
        bits.append(f"{h}p")
    elif fmt.get("format_note"):
        bits.append(str(fmt["format_note"]))
    else:
        bits.append(str(fmt.get("format_id") or "stream"))
    if fps and isinstance(fps, (int, float)) and fps >= 48:
        bits.append(f"{int(fps)}fps")
    if protocol == "hls":
        bits.append("HLS")
    else:
        ext = fmt.get("ext") or "mp4"
        bits.append(str(ext).upper())
    if tbr:
        try:
            bits.append(f"{int(float(tbr))}kbps")
        except (TypeError, ValueError):
            pass
    return " · ".join(bits)


class YoutubeClient:
    """
    YouTube resolver backed by yt-dlp (same approach as streamlink/yt-dlp ecosystem).

    Focus: live + VOD URLs that browsers can play via HLS proxy or progressive proxy.
    Separate DASH video/audio pairs are skipped (no ffmpeg remux in browser path).
    """

    def __init__(
        self,
        *,
        timeout: float = 30.0,
        cookies_file: str = "",
    ):
        self.timeout = timeout
        self.cookies_file = cookies_file

    def _ydl_opts(self) -> Dict[str, Any]:
        # yt-dlp 2025+ needs a JS runtime + EJS components for full YouTube formats.
        # Node is widely available; remote ejs:github fetches the challenge solver.
        opts: Dict[str, Any] = {
            "quiet": True,
            "no_warnings": True,
            "skip_download": True,
            "noplaylist": True,
            "socket_timeout": self.timeout,
            "js_runtimes": {"node": {}},
            "remote_components": {"ejs:github"},
        }
        if self.cookies_file:
            opts["cookiefile"] = self.cookies_file
        return opts

    def _extract_sync(self, url: str) -> Dict[str, Any]:
        try:
            import yt_dlp
        except ImportError as exc:
            raise ResolveFailedError("服务器未安装 yt-dlp，无法解析 YouTube") from exc

        try:
            with yt_dlp.YoutubeDL(self._ydl_opts()) as ydl:
                info = ydl.extract_info(url, download=False)
        except Exception as exc:  # noqa: BLE001
            msg = str(exc)
            low = msg.lower()
            if "sign in" in low or "login required" in low or "age" in low or "confirm your age" in low:
                raise LoginRequiredError(
                    "该 YouTube 视频需要登录/年龄确认。请在服务器配置 YOUTUBE_COOKIES_FILE（Netscape cookies 文件）。"
                ) from exc
            if "not available in your country" in low or "geo" in low or "blocked it in your country" in low:
                raise GeoRestrictedError(
                    "YouTube 地区限制：服务器出口 IP 无法访问该内容。请把服务部署到可访问的地区。"
                ) from exc
            if "live event will begin" in low or "premier" in low or "is offline" in low:
                raise NotLiveError(f"直播未开始或频道离线：{msg}") from exc
            if "private video" in low or "video unavailable" in low:
                raise ResolveFailedError(f"视频不可用：{msg}") from exc
            raise ResolveFailedError(f"yt-dlp 解析失败：{msg}") from exc

        if not info:
            raise ResolveFailedError("yt-dlp 未返回信息")

        # Playlist edge case
        if info.get("_type") == "playlist" and info.get("entries"):
            entry = next((e for e in info["entries"] if e), None)
            if not entry:
                raise ResolveFailedError("播放列表为空")
            info = entry

        return info

    def _pick_qualities(self, info: Dict[str, Any]) -> List[QualityStream]:
        formats = list(info.get("formats") or [])
        qualities: List[QualityStream] = []
        seen_keys: set[str] = set()

        # 1) HLS (best for live + proxy). Live m3u8 usually muxes A/V even when
        # yt-dlp reports codecs oddly — keep any video-bearing HLS URL.
        hls_fmts = [
            f
            for f in formats
            if f.get("url") and _is_hls_format(f) and (_has_video(f) or _height(f) > 0)
        ]
        candidates_hls = hls_fmts

        for fmt in sorted(candidates_hls, key=lambda f: (_height(f), f.get("tbr") or 0), reverse=True):
            h = _height(fmt)
            key = f"hls-{h}-{fmt.get('format_id')}"
            if key in seen_keys:
                continue
            # de-dupe same height keep highest tbr already sorted
            height_key = f"hls-h{h}"
            if height_key in seen_keys and h > 0:
                continue
            seen_keys.add(key)
            if h > 0:
                seen_keys.add(height_key)
            qualities.append(
                QualityStream(
                    label=_label_for(fmt, protocol="hls"),
                    name=str(fmt.get("format_id") or f"hls-{h}"),
                    direct_url=str(fmt["url"]),
                    protocol="hls",
                )
            )

        # 2) Progressive combined (single URL, browser can play mp4/webm)
        progressive = [
            f
            for f in formats
            if f.get("url")
            and not _is_hls_format(f)
            and _has_video(f)
            and _has_audio(f)
            and (f.get("protocol") or "https").startswith("http")
        ]
        for fmt in sorted(progressive, key=lambda f: (_height(f), f.get("tbr") or 0), reverse=True):
            h = _height(fmt)
            height_key = f"prog-h{h}"
            if height_key in seen_keys and h > 0:
                continue
            seen_keys.add(height_key)
            qualities.append(
                QualityStream(
                    label=_label_for(fmt, protocol="progressive"),
                    name=str(fmt.get("format_id") or f"http-{h}"),
                    direct_url=str(fmt["url"]),
                    protocol="progressive",
                )
            )

        # 3) Fallback: info['url'] if present
        if not qualities and info.get("url"):
            url = str(info["url"])
            proto = "hls" if ".m3u8" in url else "progressive"
            qualities.append(
                QualityStream(
                    label="default · HLS" if proto == "hls" else "default",
                    name="default",
                    direct_url=url,
                    protocol=proto,
                )
            )

        if not qualities:
            raise ResolveFailedError(
                "未找到浏览器可直接播放的格式（需要合并音视频的 HLS/MP4）。"
                "可尝试配置 cookies，或该内容仅有分离 DASH 流。"
            )

        # Sort: height desc, prefer hls slightly for live
        def sort_key(q: QualityStream) -> tuple:
            m = re.search(r"(\d{3,4})p", q.label)
            h = int(m.group(1)) if m else 0
            hls_bonus = 1 if q.protocol == "hls" else 0
            return (h, hls_bonus)

        qualities.sort(key=sort_key, reverse=True)
        return qualities

    async def resolve(self, url: str) -> ResolveResult:
        if not is_youtube_url(url):
            raise SoopError("不是有效的 YouTube 链接")

        info = await asyncio.to_thread(self._extract_sync, url)
        qualities = self._pick_qualities(info)

        video_id = str(info.get("id") or _video_id_hint(url))
        is_live = bool(info.get("is_live") or info.get("live_status") in {"is_live", "post_live"})
        # upcoming
        if info.get("live_status") == "is_upcoming":
            raise NotLiveError("直播尚未开始（upcoming）")

        channel = (
            info.get("channel_id")
            or info.get("uploader_id")
            or info.get("channel")
            or info.get("uploader")
            or "youtube"
        )
        author = info.get("channel") or info.get("uploader")
        title = info.get("title")
        webpage = info.get("webpage_url") or url

        return ResolveResult(
            channel=str(channel),
            bno=video_id,
            title=str(title) if title else None,
            author=str(author) if author else None,
            password_protected=False,
            platform="youtube",
            is_live=is_live,
            webpage_url=str(webpage) if webpage else url,
            qualities=qualities,
        )
