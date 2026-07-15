from __future__ import annotations

import logging
import re
from typing import Any
from urllib.parse import urlencode, urlparse, urlunparse, parse_qsl

import httpx

from .models import (
    GeoRestrictedError,
    LoginRequiredError,
    NotLiveError,
    PasswordRequiredError,
    QualityStream,
    ResolveFailedError,
    ResolveResult,
    SoopError,
)

log = logging.getLogger(__name__)

URL_RE = re.compile(
    r"https?://play\.(sooplive\.com|sooplive\.co\.kr|afreecatv\.com)/(?P<channel>[\w-]+)(?:/(?P<bno>\d+))?",
    re.IGNORECASE,
)
BNO_RE = re.compile(r"window\.nBroadNo\s*=\s*(?P<bno>\d+);")

CHANNEL_API_URL = "https://live.sooplive.com/afreeca/player_live_api.php"
LOGIN_URL = "https://login.sooplive.com/app/LoginAction.php"
AUTH_CHECK_URL = "https://afevent2.sooplive.com/api/get_private_info.php"

CHANNEL_RESULT_OK = 1
CHANNEL_LOGIN_REQUIRED = -6
STREAM_PASSWORD_PROTECTED = "Y"

CDN_TYPE_MAPPING = {
    "gs_cdn": "gs_cdn_pc_web",
    "lg_cdn": "lg_cdn_pc_web",
}

CHANNEL_API_DATA_COMMON = {
    "from_api": "0",
    "mode": "landing",
    "player_type": "html5",
    "stream_type": "common",
}

# Result codes that often indicate region/copyright blocks (best-effort)
GEO_HINT_CODES = {-3, -16, -17}


def parse_soop_url(url: str) -> tuple[str, str | None]:
    """Return (channel, bno|None) from a play.sooplive URL."""
    url = url.strip()
    match = URL_RE.match(url)
    if not match:
        raise SoopError("无效的 SOOP 直播链接，示例：https://play.sooplive.com/channel/123456")
    return match.group("channel"), match.group("bno")


def map_cdn_return_type(cdn: str | None) -> str:
    if not cdn:
        return "gs_cdn_pc_web"
    for key, mapped in CDN_TYPE_MAPPING.items():
        if key in cdn:
            return mapped
    return cdn


def append_query(url: str, **params: str) -> str:
    parsed = urlparse(url)
    query = dict(parse_qsl(parsed.query, keep_blank_values=True))
    for k, v in params.items():
        if v is not None:
            query[k] = v
    return urlunparse(parsed._replace(query=urlencode(query)))


class SoopClient:
    def __init__(
        self,
        *,
        timeout: float = 20.0,
        username: str = "",
        password: str = "",
        cookies: dict[str, str] | None = None,
    ):
        self.timeout = timeout
        self.username = username
        self.password = password
        self._client = httpx.AsyncClient(
            timeout=httpx.Timeout(timeout, connect=10.0, read=60.0, write=30.0, pool=60.0),
            limits=httpx.Limits(max_connections=100, max_keepalive_connections=40, keepalive_expiry=30.0),
            follow_redirects=True,
            headers={
                "User-Agent": (
                    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
                    "AppleWebKit/537.36 (KHTML, like Gecko) "
                    "Chrome/131.0.0.0 Safari/537.36"
                ),
                "Origin": "https://play.sooplive.com",
                "Referer": "https://play.sooplive.com/",
            },
            cookies=cookies or {},
        )
        self._logged_in = False

    async def aclose(self) -> None:
        await self._client.aclose()

    async def __aenter__(self) -> SoopClient:
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.aclose()

    async def ensure_login(self) -> bool:
        if self._logged_in:
            return True
        if self._client.cookies:
            if await self._check_auth():
                self._logged_in = True
                return True
        if self.username and self.password:
            ok = await self._login(self.username, self.password)
            self._logged_in = ok
            return ok
        return False

    async def _check_auth(self) -> bool:
        try:
            resp = await self._client.get(AUTH_CHECK_URL)
            resp.raise_for_status()
            data = resp.json()
            login_id = data.get("CHANNEL", {}).get("LOGIN_ID", "")
            return bool(login_id)
        except Exception as exc:  # noqa: BLE001
            log.debug("auth check failed: %s", exc)
            return False

    async def _login(self, username: str, password: str) -> bool:
        resp = await self._client.post(
            LOGIN_URL,
            data={
                "szWork": "login",
                "szType": "json",
                "szUid": username,
                "szPassword": password,
                "isSaveId": "true",
                "isSavePw": "false",
                "isSaveJoin": "false",
                "isLoginRetain": "Y",
            },
        )
        resp.raise_for_status()
        data = resp.json()
        result = int(data.get("RESULT", 0))
        return result == 1

    async def _fetch_bno_from_page(self, channel: str) -> str:
        url = f"https://play.sooplive.com/{channel}"
        resp = await self._client.get(url, headers={"Referer": url})
        resp.raise_for_status()
        match = BNO_RE.search(resp.text)
        if not match:
            raise NotLiveError("未找到场次号，主播可能未开播")
        return match.group("bno")

    async def _channel_api(self, data: dict[str, str], referer: str) -> dict[str, Any]:
        resp = await self._client.post(
            CHANNEL_API_URL,
            data=data,
            headers={"Referer": referer, "Origin": "https://play.sooplive.com"},
        )
        resp.raise_for_status()
        payload = resp.json()
        channel = payload.get("CHANNEL")
        if not isinstance(channel, dict):
            raise ResolveFailedError("SOOP API 返回异常（无 CHANNEL）")
        return channel

    async def get_channel_info(self, channel: str, bno: str, pwd: str = "") -> dict[str, Any]:
        referer = f"https://play.sooplive.com/{channel}/{bno}"
        return await self._channel_api(
            {
                **CHANNEL_API_DATA_COMMON,
                "type": "live",
                "bid": channel,
                "bno": bno,
                "pwd": pwd or "",
            },
            referer=referer,
        )

    async def get_aid(self, channel: str, bno: str, quality: str, pwd: str = "") -> str | None:
        referer = f"https://play.sooplive.com/{channel}/{bno}"
        info = await self._channel_api(
            {
                **CHANNEL_API_DATA_COMMON,
                "type": "aid",
                "bid": channel,
                "bno": bno,
                "pwd": pwd or "",
                "quality": quality,
            },
            referer=referer,
        )
        result = int(info.get("RESULT", 0))
        if result != CHANNEL_RESULT_OK:
            return None
        aid = info.get("AID")
        return str(aid) if aid else None

    async def get_view_url(self, rmd: str, cdn: str | None, bno: str, quality: str) -> str | None:
        return_type = map_cdn_return_type(cdn)
        rmd = rmd.rstrip("/")
        url = f"{rmd}/broad_stream_assign.html"
        resp = await self._client.get(
            url,
            params={
                "return_type": return_type,
                "broad_key": f"{bno}-common-{quality}-hls",
            },
            headers={"Referer": "https://play.sooplive.com/"},
        )
        resp.raise_for_status()
        data = resp.json()
        view_url = data.get("view_url")
        status = data.get("stream_status")
        if not view_url:
            log.debug("no view_url for quality=%s status=%s data=%s", quality, status, data)
            return None
        return str(view_url)

    def _raise_for_result(self, result: int, bpwd: str | None) -> None:
        if result == CHANNEL_RESULT_OK:
            return
        if result == CHANNEL_LOGIN_REQUIRED:
            raise LoginRequiredError("该直播需要登录后才能观看，请在服务器配置 SOOP_USERNAME / SOOP_PASSWORD")
        if result in GEO_HINT_CODES:
            raise GeoRestrictedError(
                "疑似版权/地区限制（服务器出口 IP 不被允许）。"
                "请将本服务部署到可正常打开 SOOP 的地区/VPS，并使用代理播放模式。"
            )
        if bpwd == STREAM_PASSWORD_PROTECTED:
            raise PasswordRequiredError("该直播有密码保护，请提供 stream_password")
        if result == 0 or result == -1:
            raise NotLiveError("主播未开播或场次无效")
        raise ResolveFailedError(f"SOOP 返回错误码 RESULT={result}")

    async def resolve(
        self,
        url: str,
        *,
        stream_password: str = "",
    ) -> ResolveResult:
        channel, bno = parse_soop_url(url)
        await self.ensure_login()

        if not bno:
            bno = await self._fetch_bno_from_page(channel)

        info = await self.get_channel_info(channel, bno, stream_password)
        result = int(info.get("RESULT", 0))
        bpwd = info.get("BPWD")
        self._raise_for_result(result, bpwd)

        resolved_bno = str(info.get("BNO") or bno)
        rmd = info.get("RMD")
        cdn = info.get("CDN")
        title = info.get("TITLE")
        author = info.get("BJNICK")
        viewpreset = info.get("VIEWPRESET") or []

        if not rmd:
            # Sometimes geo/block shows OK-ish structure without RMD
            raise GeoRestrictedError(
                "未能获取流媒体节点（RMD 为空）。常见原因：服务器 IP 版权限制、未开播、或需登录。"
            )

        qualities: list[QualityStream] = []
        for item in viewpreset:
            name = item.get("name")
            label = item.get("label") or name
            if not name or name == "auto":
                continue
            aid = await self.get_aid(channel, resolved_bno, name, stream_password)
            if not aid:
                continue
            view_url = await self.get_view_url(str(rmd), str(cdn) if cdn else None, resolved_bno, name)
            if not view_url:
                continue
            direct = append_query(view_url, aid=aid)
            qualities.append(
                QualityStream(
                    label=str(label),
                    name=str(name),
                    direct_url=direct,
                    protocol="hls",
                )
            )

        if not qualities:
            if bpwd == STREAM_PASSWORD_PROTECTED and not stream_password:
                raise PasswordRequiredError("该直播有密码保护，请提供 stream_password")
            raise ResolveFailedError(
                "未能获取任何清晰度。可能原因：版权地区限制、CDN 拒绝、或直播刚结束。"
            )

        return ResolveResult(
            channel=channel,
            bno=resolved_bno,
            title=str(title) if title else None,
            author=str(author) if author else None,
            cdn=str(cdn) if cdn else None,
            password_protected=bpwd == STREAM_PASSWORD_PROTECTED,
            platform="soop",
            is_live=True,
            webpage_url=f"https://play.sooplive.com/{channel}/{resolved_bno}",
            qualities=qualities,
        )

    async def fetch_bytes(
        self,
        url: str,
        *,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, str], bytes]:
        resp = await self._client.get(url, headers=headers or {})
        out_headers = {
            k: v
            for k, v in resp.headers.items()
            if k.lower() in {
                "content-type",
                "content-length",
                "cache-control",
                "expires",
                "last-modified",
                "etag",
            }
        }
        return resp.status_code, out_headers, resp.content

    def stream(self, method: str, url: str, **kwargs):
        """Expose httpx stream context manager for proxying segments."""
        return self._client.stream(method, url, **kwargs)
