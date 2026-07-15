from app.main import append_access_token, resolve_public_base
from app.config import Settings


class _URL:
    scheme = "http"
    netloc = "127.0.0.1:8080"


class _Request:
    def __init__(self, headers=None, settings=None):
        self.headers = headers or {}
        self.url = _URL()
        self.base_url = "http://127.0.0.1:8080/"
        self.app = type("A", (), {"state": type("S", (), {"settings": settings})()})()


def test_public_base_from_env():
    s = Settings(public_base_url="https://xxxxxxxxx.xxx/")
    req = _Request(settings=s)
    assert resolve_public_base(req, s) == "https://xxxxxxxxx.xxx"


def test_public_base_from_forwarded_headers():
    s = Settings(public_base_url="")
    req = _Request(
        headers={
            "x-forwarded-proto": "https",
            "x-forwarded-host": "live.example.com",
        },
        settings=s,
    )
    assert resolve_public_base(req, s) == "https://live.example.com"


def test_append_access_token():
    s = Settings(access_token="secret")
    assert "token=secret" in append_access_token("https://x/a/b.m3u8", s)
    assert "token=secret" in append_access_token("https://x/a?x=1", s)
