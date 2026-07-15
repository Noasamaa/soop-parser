from __future__ import annotations

import logging
from contextlib import asynccontextmanager
from pathlib import Path
from typing import List, Optional
from urllib.parse import quote

import httpx
from fastapi import Depends, FastAPI, Header, HTTPException, Query, Request, Response
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from starlette.datastructures import MutableHeaders

from app.config import Settings, get_settings
from app.soop.client import SoopClient
from app.soop.models import (
    ErrorResponse,
    GeoRestrictedError,
    LoginRequiredError,
    NotLiveError,
    PasswordRequiredError,
    QualityStream,
    ResolveFailedError,
    ResolveRequest,
    ResolveResponse,
    ResolveResult,
    SoopError,
)
from app.soop.proxy import (
    PlaySessionStore,
    decode_proxied_url,
    is_allowed_upstream,
    is_hls_playlist_url,
    rewrite_m3u8,
    upstream_headers,
)
from app.youtube.client import YoutubeClient, is_youtube_url

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("live-parser")

STATIC_DIR = Path(__file__).resolve().parent / "static"


def resolve_public_base(request: Request, settings: Optional[Settings] = None) -> str:
    """
    Absolute origin for play URLs returned to clients (poplayer / VLC / apps).

    Priority:
      1. PUBLIC_BASE_URL env (recommended behind reverse proxy)
      2. X-Forwarded-Proto + X-Forwarded-Host (or Host)
      3. request.base_url
    """
    settings = settings or getattr(request.app.state, "settings", None) or get_settings()
    configured = (settings.public_base_url or "").strip().rstrip("/")
    if configured:
        return configured

    proto = (request.headers.get("x-forwarded-proto") or request.url.scheme or "https").split(",")[0].strip()
    host = (
        request.headers.get("x-forwarded-host")
        or request.headers.get("host")
        or request.url.netloc
    )
    host = (host or "").split(",")[0].strip()
    if host:
        return f"{proto}://{host}".rstrip("/")

    return str(request.base_url).rstrip("/")


def append_access_token(path_or_url: str, settings: Settings) -> str:
    token = (settings.access_token or "").strip()
    if not token:
        return path_or_url
    sep = "&" if "?" in path_or_url else "?"
    return f"{path_or_url}{sep}token={quote(token, safe='')}"


def hls_proxy_base(request: Request, token: str, settings: Settings) -> str:
    """Absolute prefix for rewritten m3u8 media lines (must be same public domain)."""
    base = resolve_public_base(request, settings)
    path = f"{base}/api/hls/{token}/proxy"
    if settings.access_token:
        return f"{path}?token={quote(settings.access_token, safe='')}&u="
    return f"{path}?u="


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = get_settings()
    app.state.settings = settings
    app.state.sessions = PlaySessionStore(ttl=settings.play_token_ttl)
    app.state.soop = SoopClient(
        timeout=settings.http_timeout,
        username=settings.soop_username,
        password=settings.soop_password,
    )
    app.state.youtube = YoutubeClient(
        timeout=max(settings.http_timeout, 30.0),
        cookies_file=settings.youtube_cookies_file,
    )
    # Shared httpx client for CDN proxy (PotPlayer opens many parallel segment requests)
    timeout = httpx.Timeout(settings.http_timeout, connect=10.0, read=60.0, write=30.0, pool=60.0)
    limits = httpx.Limits(max_connections=100, max_keepalive_connections=40, keepalive_expiry=30.0)
    app.state.http = httpx.AsyncClient(
        timeout=timeout,
        limits=limits,
        follow_redirects=True,
        headers={
            "User-Agent": (
                "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/131.0.0.0 Safari/537.36"
            ),
        },
    )
    if settings.soop_username and settings.soop_password:
        try:
            ok = await app.state.soop.ensure_login()
            log.info("SOOP login on startup: %s", "ok" if ok else "failed")
        except Exception as exc:  # noqa: BLE001
            log.warning("SOOP login on startup error: %s", exc)
    if settings.youtube_cookies_file:
        log.info("YouTube cookies file configured: %s", settings.youtube_cookies_file)
    yield
    await app.state.soop.aclose()
    await app.state.http.aclose()


app = FastAPI(
    title="Live Parser",
    description="Self-hosted SOOP + YouTube live/VOD resolver with HLS/media proxy",
    version="1.2.1",
    lifespan=lifespan,
)

# Allow external players / web apps (poplayer on another origin) to call APIs & play streams.
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=False,
    allow_methods=["*"],
    allow_headers=["*"],
    expose_headers=["Content-Length", "Content-Range", "Accept-Ranges"],
)


@app.middleware("http")
async def head_method_support(request: Request, call_next):
    """
    PotPlayer and many desktop players probe resources with HTTP HEAD.
    StaticFiles mount at '/' breaks FastAPI auto-HEAD, so rewrite HEAD→GET
    and return headers only (no body).
    """
    if request.method != "HEAD":
        return await call_next(request)

    request.scope["method"] = "GET"
    response = await call_next(request)

    # Drain streaming body so upstream connections close cleanly
    if hasattr(response, "body_iterator"):
        async for _ in response.body_iterator:
            pass
        headers = MutableHeaders(response.headers)
        headers.pop("content-length", None)
        return Response(status_code=response.status_code, headers=dict(headers))

    headers = MutableHeaders(getattr(response, "headers", {}))
    headers.pop("content-length", None)
    return Response(status_code=response.status_code, headers=dict(headers))


def require_access(
    request: Request,
    x_access_token: Optional[str] = Header(default=None),
    access_token: Optional[str] = Query(default=None, alias="token"),
) -> None:
    """Validate ACCESS_TOKEN via header X-Access-Token, ?token=, or Bearer."""
    settings: Settings = request.app.state.settings
    expected = (settings.access_token or "").strip()
    if not expected:
        return
    provided = (x_access_token or access_token or "").strip()
    auth = request.headers.get("authorization", "")
    if auth.lower().startswith("bearer "):
        provided = provided or auth[7:].strip()
    if provided != expected:
        raise HTTPException(status_code=401, detail="无效的访问令牌。请配置请求头 X-Access-Token。")


def soop_error_response(exc: SoopError) -> JSONResponse:
    return JSONResponse(
        status_code=exc.status_code,
        content=ErrorResponse(ok=False, code=exc.code, message=exc.message).model_dump(),
    )


@app.exception_handler(SoopError)
async def handle_soop_error(_request: Request, exc: SoopError):
    return soop_error_response(exc)


def detect_platform(url: str) -> str:
    u = url.strip()
    if is_youtube_url(u):
        return "youtube"
    if "sooplive." in u or "afreecatv.com" in u:
        return "soop"
    # Try soop parser's URL regex via exception path later
    if "play." in u and ("soop" in u or "afreeca" in u):
        return "soop"
    return "unknown"


def attach_proxy_urls(
    result: ResolveResult,
    sessions: PlaySessionStore,
    *,
    proxy: bool,
    public_base: str,
    settings: Settings,
) -> List[QualityStream]:
    """
    Build play_url list. In proxy mode, URLs are absolute under public_base so
    external players (poplayer) always hit YOUR server, not SOOP/YouTube CDN.
    """
    base = public_base.rstrip("/")
    qualities: List[QualityStream] = []
    for q in result.qualities:
        play_url = q.direct_url
        protocol = q.protocol or "hls"
        if proxy and q.direct_url:
            session = sessions.create(
                q.direct_url,
                channel=result.channel,
                quality=q.name,
                label=q.label,
                platform=result.platform,
                media_type=protocol,
            )
            if protocol == "progressive":
                play_url = f"{base}/api/media/{session.token}"
            else:
                play_url = f"{base}/api/hls/{session.token}/playlist.m3u8"
            play_url = append_access_token(play_url, settings)
        qualities.append(
            QualityStream(
                label=q.label,
                name=q.name,
                direct_url=None if proxy else q.direct_url,
                play_url=play_url,
                protocol=protocol,
            )
        )
    return qualities


@app.get("/health")
async def health():
    return {"ok": True, "service": "live-parser", "platforms": ["soop", "youtube"]}


@app.get("/api/config")
async def api_config(request: Request, _: None = Depends(require_access)):
    settings: Settings = request.app.state.settings
    return {
        "auth_required": bool(settings.access_token),
        "login_configured": bool(settings.soop_username and settings.soop_password),
        "youtube_cookies_configured": bool(settings.youtube_cookies_file),
        "play_token_ttl": settings.play_token_ttl,
        "platforms": ["soop", "youtube"],
        "public_base_url": resolve_public_base(request, settings),
        "yt_dlp_hint": "yt-dlp>=2026.7.4 (2026.07.04 lineage)",
    }


@app.post("/api/resolve", response_model=ResolveResponse)
async def api_resolve(
    body: ResolveRequest,
    request: Request,
    _: None = Depends(require_access),
):
    sessions: PlaySessionStore = request.app.state.sessions
    platform = detect_platform(body.url)

    try:
        if platform == "youtube":
            client: YoutubeClient = request.app.state.youtube
            result = await client.resolve(body.url)
        elif platform == "soop":
            soop: SoopClient = request.app.state.soop
            result = await soop.resolve(body.url, stream_password=body.stream_password)
        else:
            # Last resort: try SOOP URL parse then YouTube
            from app.soop.client import parse_soop_url

            try:
                parse_soop_url(body.url)
                soop = request.app.state.soop
                result = await soop.resolve(body.url, stream_password=body.stream_password)
            except SoopError:
                if is_youtube_url(body.url):
                    result = await request.app.state.youtube.resolve(body.url)
                else:
                    raise SoopError(
                        "无法识别链接。支持：\n"
                        "· SOOP: https://play.sooplive.com/频道/场次\n"
                        "· YouTube: https://www.youtube.com/watch?v=... 或 /live/..."
                    )
    except (
        NotLiveError,
        LoginRequiredError,
        PasswordRequiredError,
        GeoRestrictedError,
        ResolveFailedError,
        SoopError,
    ):
        raise
    except httpx.HTTPError as exc:
        log.exception("upstream http error")
        raise ResolveFailedError(f"请求上游失败: {exc}") from exc
    except Exception as exc:  # noqa: BLE001
        log.exception("resolve failed")
        raise ResolveFailedError(f"解析失败: {exc}") from exc

    settings: Settings = request.app.state.settings
    public_base = resolve_public_base(request, settings)
    qualities = attach_proxy_urls(
        result,
        sessions,
        proxy=body.proxy,
        public_base=public_base,
        settings=settings,
    )

    return ResolveResponse(
        ok=True,
        platform=result.platform,
        is_live=result.is_live,
        channel=result.channel,
        bno=result.bno,
        title=result.title,
        author=result.author,
        password_protected=result.password_protected,
        qualities=qualities,
    )


async def _fetch_upstream(
    request: Request,
    url: str,
    *,
    platform: str,
    channel: str,
) -> tuple[int, dict, bytes]:
    headers = upstream_headers(platform, channel)
    if platform == "soop":
        client: SoopClient = request.app.state.soop
        return await client.fetch_bytes(url, headers=headers)
    http: httpx.AsyncClient = request.app.state.http
    resp = await http.get(url, headers=headers)
    out_headers = {
        k: v
        for k, v in resp.headers.items()
        if k.lower()
        in {
            "content-type",
            "content-length",
            "cache-control",
            "accept-ranges",
            "content-range",
        }
    }
    return resp.status_code, out_headers, resp.content


@app.get("/api/hls/{token}/playlist.m3u8")
async def hls_playlist(
    token: str,
    request: Request,
    _: None = Depends(require_access),
):
    sessions: PlaySessionStore = request.app.state.sessions
    session = sessions.get(token)
    if not session:
        raise HTTPException(status_code=404, detail="播放会话不存在或已过期，请重新解析")
    if session.media_type == "progressive":
        raise HTTPException(status_code=400, detail="该会话是 progressive 媒体，请使用 /api/media/{token}")

    upstream = session.upstream_url
    if not is_allowed_upstream(upstream):
        raise HTTPException(status_code=400, detail="上游地址不被允许")

    try:
        status, _headers, content = await _fetch_upstream(
            request,
            upstream,
            platform=session.platform,
            channel=session.channel,
        )
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"拉取 playlist 失败: {exc}") from exc

    if status >= 400:
        text_preview = content[:200].decode("utf-8", errors="ignore")
        log.warning("playlist status=%s preview=%s", status, text_preview)
        if status in (403, 451):
            raise HTTPException(
                status_code=451,
                detail="CDN 拒绝访问（可能是服务器 IP 地区/版权限制）。请更换 VPS 地区。",
            )
        raise HTTPException(status_code=502, detail=f"上游 playlist HTTP {status}")

    text = content.decode("utf-8", errors="replace")
    settings: Settings = request.app.state.settings
    # Absolute segment URLs so poplayer / external HLS clients keep using YOUR domain
    proxy_base = hls_proxy_base(request, token, settings)

    rewritten = rewrite_m3u8(text, base_url=upstream, proxy_base=proxy_base)
    return Response(
        content=rewritten,
        media_type="application/vnd.apple.mpegurl",
        headers={
            "Cache-Control": "no-store",
            "Access-Control-Allow-Origin": "*",
        },
    )


@app.get("/api/hls/{token}/proxy")
async def hls_proxy(
    token: str,
    request: Request,
    u: str = Query(..., description="Upstream absolute URL"),
    _: None = Depends(require_access),
):
    sessions: PlaySessionStore = request.app.state.sessions
    session = sessions.get(token)
    if not session:
        raise HTTPException(status_code=404, detail="播放会话不存在或已过期，请重新解析")

    upstream = decode_proxied_url(u)
    if not is_allowed_upstream(upstream):
        raise HTTPException(status_code=400, detail="上游 host 不在白名单（防 SSRF）")

    headers = upstream_headers(session.platform, session.channel)

    if is_hls_playlist_url(upstream):
        try:
            status, _hdrs, content = await _fetch_upstream(
                request,
                upstream,
                platform=session.platform,
                channel=session.channel,
            )
        except httpx.HTTPError as exc:
            raise HTTPException(status_code=502, detail=f"拉取子 playlist 失败: {exc}") from exc
        if status >= 400:
            raise HTTPException(status_code=502, detail=f"上游 HTTP {status}")
        text = content.decode("utf-8", errors="replace")
        settings: Settings = request.app.state.settings
        proxy_base = hls_proxy_base(request, token, settings)
        rewritten = rewrite_m3u8(text, base_url=upstream, proxy_base=proxy_base)
        return Response(
            content=rewritten,
            media_type="application/vnd.apple.mpegurl",
            headers={"Cache-Control": "no-store", "Access-Control-Allow-Origin": "*"},
        )

    async def byte_iter():
        try:
            if session.platform == "soop":
                client: SoopClient = request.app.state.soop
                stream_cm = client.stream("GET", upstream, headers=headers)
            else:
                http: httpx.AsyncClient = request.app.state.http
                stream_cm = http.stream("GET", upstream, headers=headers)
            async with stream_cm as resp:
                if resp.status_code >= 400:
                    log.warning("segment status=%s url=%s", resp.status_code, upstream[:120])
                    return
                async for chunk in resp.aiter_bytes():
                    yield chunk
        except httpx.HTTPError as exc:
            log.warning("segment error: %s", exc)
            return

    return StreamingResponse(
        byte_iter(),
        media_type="video/mp2t",
        headers={
            "Cache-Control": "no-store, no-cache, must-revalidate",
            "Access-Control-Allow-Origin": "*",
            "Accept-Ranges": "bytes",
        },
    )


@app.get("/api/media/{token}")
async def media_proxy(
    token: str,
    request: Request,
    _: None = Depends(require_access),
):
    """Proxy progressive (non-HLS) media for YouTube combined mp4/webm etc."""
    sessions: PlaySessionStore = request.app.state.sessions
    session = sessions.get(token)
    if not session:
        raise HTTPException(status_code=404, detail="播放会话不存在或已过期，请重新解析")

    upstream = session.upstream_url
    if not is_allowed_upstream(upstream):
        raise HTTPException(status_code=400, detail="上游地址不被允许")

    headers = upstream_headers(session.platform, session.channel)
    # Forward Range for seeking
    range_header = request.headers.get("range")
    if range_header:
        headers = {**headers, "Range": range_header}

    http: httpx.AsyncClient = request.app.state.http

    try:
        req = http.build_request("GET", upstream, headers=headers)
        resp = await http.send(req, stream=True)
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"拉取媒体失败: {exc}") from exc

    if resp.status_code >= 400:
        await resp.aclose()
        if resp.status_code in (403, 451):
            raise HTTPException(status_code=451, detail="CDN 拒绝访问，可能是地区限制")
        raise HTTPException(status_code=502, detail=f"上游媒体 HTTP {resp.status_code}")

    content_type = resp.headers.get("content-type") or "video/mp4"
    out_headers = {
        "Cache-Control": "no-store",
        "Access-Control-Allow-Origin": "*",
        "Accept-Ranges": resp.headers.get("accept-ranges", "bytes"),
    }
    if resp.headers.get("content-length"):
        out_headers["Content-Length"] = resp.headers["content-length"]
    if resp.headers.get("content-range"):
        out_headers["Content-Range"] = resp.headers["content-range"]

    async def byte_iter():
        try:
            async for chunk in resp.aiter_bytes():
                yield chunk
        finally:
            await resp.aclose()

    return StreamingResponse(
        byte_iter(),
        status_code=resp.status_code,
        media_type=content_type,
        headers=out_headers,
    )


# Static UI last so API routes win
if STATIC_DIR.is_dir():
    app.mount("/", StaticFiles(directory=str(STATIC_DIR), html=True), name="static")
