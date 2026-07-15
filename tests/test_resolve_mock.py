import httpx
import pytest
import respx

from app.soop.client import CHANNEL_API_URL, SoopClient


@pytest.mark.asyncio
@respx.mock
async def test_resolve_success():
    channel = "loltw"
    bno = "295590617"
    play_url = f"https://play.sooplive.com/{channel}/{bno}"

    respx.post(CHANNEL_API_URL).mock(
        side_effect=[
            # type=live
            httpx.Response(
                200,
                json={
                    "CHANNEL": {
                        "RESULT": 1,
                        "BNO": bno,
                        "BJNICK": "LOL TW",
                        "TITLE": "Test Match",
                        "RMD": "https://livestream-manager.sooplive.com",
                        "CDN": "gs_cdn",
                        "BPWD": "N",
                        "VIEWPRESET": [
                            {"label": "1080p", "name": "original"},
                            {"label": "auto", "name": "auto"},
                        ],
                    }
                },
            ),
            # type=aid
            httpx.Response(
                200,
                json={"CHANNEL": {"RESULT": 1, "AID": "aid-token-1"}},
            ),
        ]
    )

    respx.get("https://livestream-manager.sooplive.com/broad_stream_assign.html").mock(
        return_value=httpx.Response(
            200,
            json={
                "view_url": "https://edge.sooplive.com/live/stream.m3u8",
                "stream_status": "START",
            },
        )
    )

    async with SoopClient() as client:
        result = await client.resolve(play_url)

    assert result.channel == channel
    assert result.bno == bno
    assert result.title == "Test Match"
    assert result.author == "LOL TW"
    assert len(result.qualities) == 1
    assert result.qualities[0].label == "1080p"
    assert "aid=aid-token-1" in (result.qualities[0].direct_url or "")


@pytest.mark.asyncio
@respx.mock
async def test_resolve_not_live():
    respx.post(CHANNEL_API_URL).mock(
        return_value=httpx.Response(
            200,
            json={"CHANNEL": {"RESULT": -1, "BPWD": "N"}},
        )
    )

    async with SoopClient() as client:
        with pytest.raises(Exception) as ei:
            await client.resolve("https://play.sooplive.com/foo/1")
        assert "未开播" in str(ei.value) or "not_live" in getattr(ei.value, "code", "")
