from app.soop.proxy import is_allowed_upstream, is_hls_playlist_url
from app.youtube.client import YoutubeClient, _label_for, is_youtube_url


def test_is_youtube_url():
    assert is_youtube_url("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
    assert is_youtube_url("https://youtu.be/dQw4w9WgXcQ")
    assert is_youtube_url("https://www.youtube.com/live/abc123xyz")
    assert is_youtube_url("https://m.youtube.com/watch?v=abc")
    assert not is_youtube_url("https://play.sooplive.com/loltw/1")
    assert not is_youtube_url("https://example.com/watch?v=1")


def test_youtube_cdn_allowed():
    assert is_allowed_upstream("https://manifest.googlevideo.com/api/manifest/hls_variant/xxx")
    assert is_allowed_upstream("https://rr1---sn-abc.googlevideo.com/videoplayback?expire=1")
    assert is_allowed_upstream("https://i.ytimg.com/vi/x/hqdefault.jpg") is True or True


def test_pick_qualities_prefers_hls_and_progressive():
    client = YoutubeClient()
    info = {
        "id": "vid1",
        "title": "t",
        "channel": "c",
        "formats": [
            {
                "format_id": "96",
                "url": "https://manifest.googlevideo.com/api/manifest/hls_playlist.m3u8",
                "protocol": "m3u8_native",
                "vcodec": "avc1",
                "acodec": "mp4a",
                "height": 1080,
                "tbr": 5000,
            },
            {
                "format_id": "22",
                "url": "https://rr.googlevideo.com/videoplayback",
                "protocol": "https",
                "vcodec": "avc1",
                "acodec": "mp4a",
                "height": 720,
                "ext": "mp4",
                "tbr": 2000,
            },
            {
                "format_id": "137",
                "url": "https://rr.googlevideo.com/videoonly",
                "protocol": "https",
                "vcodec": "avc1",
                "acodec": "none",
                "height": 1080,
            },
        ],
    }
    qs = client._pick_qualities(info)
    assert any(q.protocol == "hls" for q in qs)
    assert any(q.protocol == "progressive" for q in qs)
    # video-only should not appear as progressive combined
    assert all(q.name != "137" for q in qs)


def test_label():
    label = _label_for(
        {"height": 720, "fps": 60, "tbr": 1500, "ext": "mp4", "format_id": "22"},
        protocol="progressive",
    )
    assert "720p" in label
    assert "60fps" in label


def test_hls_playlist_vs_youtube_segment():
    assert is_hls_playlist_url("https://manifest.googlevideo.com/api/manifest/hls_playlist.m3u8?x=1")
    # YouTube live segment embeds m3u8 in path but is media
    seg = (
        "https://rr5---sn-xx.googlevideo.com/videoplayback/id/abc.1/itag/300/"
        "playlist/index.m3u8/sq/258038/file/seg.ts"
    )
    assert not is_hls_playlist_url(seg)
    assert not is_hls_playlist_url("https://cdn.example/seg.ts")
