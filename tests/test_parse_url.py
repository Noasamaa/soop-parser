import pytest

from app.soop.client import append_query, map_cdn_return_type, parse_soop_url
from app.soop.models import SoopError
from app.soop.proxy import is_allowed_upstream, rewrite_m3u8


def test_parse_soop_url_full():
    channel, bno = parse_soop_url("https://play.sooplive.com/loltw/295590617")
    assert channel == "loltw"
    assert bno == "295590617"


def test_parse_soop_url_channel_only():
    channel, bno = parse_soop_url("https://play.sooplive.com/loltw")
    assert channel == "loltw"
    assert bno is None


def test_parse_soop_url_kr():
    channel, bno = parse_soop_url("https://play.sooplive.co.kr/somebj/123")
    assert channel == "somebj"
    assert bno == "123"


def test_parse_invalid():
    with pytest.raises(SoopError):
        parse_soop_url("https://example.com/foo")


def test_map_cdn():
    assert map_cdn_return_type("gs_cdn") == "gs_cdn_pc_web"
    assert map_cdn_return_type("lg_cdn_xxx") == "lg_cdn_pc_web"
    assert map_cdn_return_type("custom") == "custom"


def test_append_query():
    url = append_query("https://cdn.example/live.m3u8?x=1", aid="abc")
    assert "aid=abc" in url
    assert "x=1" in url


def test_is_allowed_upstream():
    assert is_allowed_upstream("https://live.sooplive.com/x")
    assert is_allowed_upstream("https://foo.bar.sooplive.com/seg.ts")
    assert not is_allowed_upstream("https://evil.example/steal")
    assert not is_allowed_upstream("file:///etc/passwd")


def test_rewrite_m3u8_filters_preloading_and_rewrites():
    content = """#EXTM3U
#EXT-X-VERSION:3
#EXTINF:2.0,
segment1.ts
#EXTINF:2.0,
https://cdn.sooplive.com/preloading/x.ts
#EXTINF:2.0,
https://cdn.sooplive.com/real/seg.ts
"""
    out = rewrite_m3u8(
        content,
        base_url="https://cdn.sooplive.com/path/playlist.m3u8",
        proxy_base="/api/hls/tok/proxy?u=",
    )
    assert "preloading" not in out
    assert "/api/hls/tok/proxy?u=" in out
    assert "segment1.ts" not in out.splitlines() or any(
        line.startswith("/api/hls/") for line in out.splitlines() if line and not line.startswith("#")
    )
    # absolute segment rewritten
    assert any("real%2Fseg.ts" in line or "real/seg.ts" in line for line in out.splitlines())
