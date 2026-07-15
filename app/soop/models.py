from __future__ import annotations

from typing import List, Optional

from pydantic import BaseModel, Field


class SoopError(Exception):
    """Base error for SOOP resolve failures."""

    code: str = "soop_error"
    status_code: int = 400

    def __init__(self, message: str):
        super().__init__(message)
        self.message = message


class NotLiveError(SoopError):
    code = "not_live"
    status_code = 404


class LoginRequiredError(SoopError):
    code = "login_required"
    status_code = 401


class PasswordRequiredError(SoopError):
    code = "password_required"
    status_code = 403


class GeoRestrictedError(SoopError):
    code = "geo_restricted"
    status_code = 451


class ResolveFailedError(SoopError):
    code = "resolve_failed"
    status_code = 502


class QualityStream(BaseModel):
    label: str
    name: str
    direct_url: Optional[str] = None
    play_url: Optional[str] = None
    # hls = m3u8 playlist; progressive = single media URL (mp4/webm)
    protocol: str = "hls"


class ResolveResult(BaseModel):
    channel: str
    bno: str = ""
    title: Optional[str] = None
    author: Optional[str] = None
    cdn: Optional[str] = None
    password_protected: bool = False
    platform: str = "soop"
    is_live: bool = True
    webpage_url: Optional[str] = None
    qualities: List[QualityStream] = Field(default_factory=list)


class ResolveRequest(BaseModel):
    url: str
    stream_password: str = ""
    proxy: bool = True


class ResolveResponse(BaseModel):
    ok: bool = True
    platform: str = "soop"
    is_live: bool = True
    channel: str
    bno: str = ""
    title: Optional[str] = None
    author: Optional[str] = None
    password_protected: bool = False
    qualities: List[QualityStream] = Field(default_factory=list)


class ErrorResponse(BaseModel):
    ok: bool = False
    code: str
    message: str
