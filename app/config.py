from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    access_token: str = ""
    soop_username: str = ""
    soop_password: str = ""
    # Netscape cookies.txt for age-restricted / members-only YouTube
    youtube_cookies_file: str = ""
    # Public site origin for absolute play URLs (poplayer / external players).
    # Example: https://live.example.com  (no trailing slash)
    # If empty, derived from each request's Host / X-Forwarded-* headers.
    public_base_url: str = ""
    play_token_ttl: int = 600
    http_timeout: float = 20.0
    host: str = "0.0.0.0"
    port: int = 8080


@lru_cache
def get_settings() -> Settings:
    return Settings()
