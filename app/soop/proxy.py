from __future__ import annotations

import logging
import re
import secrets
import time
from dataclasses import dataclass, field
from urllib.parse import quote, urljoin, urlparse

log = logging.getLogger(__name__)

# Hosts allowed for reverse-proxy (SSRF protection).
ALLOWED_HOST_SUFFIXES = (
    # SOOP / Afreeca
    "sooplive.com",
    "sooplive.co.kr",
    "afreecatv.com",
    "afreeca.tv",
    "gs-cdn.com",
    "vod.sooplive.com",
    "live.sooplive.com",
    "soocdn.com",
    "afcdn.net",
    "cloudfront.net",
    "akamaihd.net",
    "akamaized.net",
    # YouTube
    "youtube.com",
    "youtu.be",
    "googlevideo.com",
    "ytimg.com",
    "ggpht.com",
    "googleusercontent.com",
    "googleapis.com",
    "gvt1.com",
)

ALLOWED_HOST_KEYWORDS = (
    "soop",
    "afreeca",
    "youtube",
    "googlevideo",
    "ytimg",
)

URI_ATTR_RE = re.compile(r'URI="([^"]+)"')


def is_allowed_upstream(url: str) -> bool:
    try:
        parsed = urlparse(url)
    except Exception:  # noqa: BLE001
        return False
    if parsed.scheme not in ("http", "https"):
        return False
    host = (parsed.hostname or "").lower()
    if not host:
        return False
    for suffix in ALLOWED_HOST_SUFFIXES:
        if host == suffix or host.endswith("." + suffix):
            return True
    for kw in ALLOWED_HOST_KEYWORDS:
        if kw in host:
            return True
    return False


def upstream_headers(platform: str, channel: str = "") -> dict[str, str]:
    if platform == "youtube":
        return {
            "Referer": "https://www.youtube.com/",
            "Origin": "https://www.youtube.com",
            "User-Agent": (
                "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/131.0.0.0 Safari/537.36"
            ),
        }
    return {
        "Referer": f"https://play.sooplive.com/{channel}" if channel else "https://play.sooplive.com/",
        "Origin": "https://play.sooplive.com",
    }


@dataclass
class PlaySession:
    token: str
    upstream_url: str
    channel: str
    quality: str
    label: str
    platform: str = "soop"
    media_type: str = "hls"  # hls | progressive
    created_at: float = field(default_factory=time.time)
    expires_at: float = 0.0

    def expired(self) -> bool:
        return time.time() > self.expires_at


class PlaySessionStore:
    def __init__(self, ttl: int = 600):
        self.ttl = ttl
        self._sessions: dict[str, PlaySession] = {}

    def create(
        self,
        upstream_url: str,
        *,
        channel: str,
        quality: str,
        label: str,
        platform: str = "soop",
        media_type: str = "hls",
    ) -> PlaySession:
        self.cleanup()
        token = secrets.token_urlsafe(18)
        now = time.time()
        session = PlaySession(
            token=token,
            upstream_url=upstream_url,
            channel=channel,
            quality=quality,
            label=label,
            platform=platform,
            media_type=media_type,
            created_at=now,
            expires_at=now + self.ttl,
        )
        self._sessions[token] = session
        return session

    def get(self, token: str) -> PlaySession | None:
        session = self._sessions.get(token)
        if not session:
            return None
        if session.expired():
            self._sessions.pop(token, None)
            return None
        # Sliding TTL: keep session alive while player is still pulling
        session.expires_at = time.time() + self.ttl
        return session

    def cleanup(self) -> None:
        now = time.time()
        dead = [k for k, s in self._sessions.items() if s.expires_at < now]
        for k in dead:
            self._sessions.pop(k, None)


def rewrite_m3u8(content: str, *, base_url: str, proxy_base: str) -> str:
    """
    Rewrite an HLS playlist so every media/playlist URI goes through our proxy.

    proxy_base example: /api/hls/{token}/proxy?u=
    Final line becomes: /api/hls/{token}/proxy?u=<urlencoded absolute url>
    """
    lines_out: list[str] = []
    for raw_line in content.splitlines():
        line = raw_line.strip()
        if not line:
            lines_out.append(raw_line)
            continue

        if line.startswith("#"):
            # Rewrite URI="..." attributes (EXT-X-KEY, EXT-X-MAP, etc.)
            def _sub(m: re.Match[str]) -> str:
                uri = m.group(1)
                if uri.startswith("data:"):
                    return m.group(0)
                abs_url = urljoin(base_url, uri)
                if "preloading" in abs_url:
                    # Drop preloading key refs if any
                    return 'URI=""'
                return f'URI="{proxy_base}{quote(abs_url, safe="")}"'

            rewritten = URI_ATTR_RE.sub(_sub, line)
            lines_out.append(rewritten)
            continue

        # Media / playlist URI line
        abs_url = urljoin(base_url, line)
        if "preloading" in abs_url:
            # Skip SOOP preloading segments
            continue
        lines_out.append(f"{proxy_base}{quote(abs_url, safe='')}")

    # Preserve trailing newline for players
    return "\n".join(lines_out) + "\n"


def decode_proxied_url(encoded: str) -> str:
    """
    Restore upstream URL from the proxy query param.

    FastAPI already percent-decodes query values once. Do NOT unquote again,
    or embedded encodings inside YouTube googlevideo signatures (%3D etc.)
    will be corrupted and the CDN returns 403.
    """
    return encoded


def is_hls_playlist_url(url: str) -> bool:
    """
    Detect HLS playlists vs media segments.

    YouTube live segment URLs often embed 'playlist' / 'm3u8' in the path
    but end with /file/seg.ts — those must NOT be treated as playlists.
    """
    try:
        parsed = urlparse(url)
    except Exception:  # noqa: BLE001
        return False
    path = (parsed.path or "").lower()
    query = (parsed.query or "").lower()

    # Explicit media extensions / YouTube live segment marker
    if path.endswith((".ts", ".m4s", ".mp4", ".m4v", ".cmfv", ".cmfa", ".aac", ".vtt")):
        return False
    if "/file/seg.ts" in path or path.endswith("seg.ts"):
        return False

    if path.endswith(".m3u8"):
        return True
    if path.endswith("/manifest") or path.endswith(".mpd"):
        return True
    # master/media playlist without extension
    if "m3u8" in path:
        return True
    if "playlist/index" in path and "/sq/" not in path:
        return True
    if "manifest/hls" in path:
        return True
    return False
