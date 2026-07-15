# Build
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/soop-parser ./cmd/soop-parser

# Runtime: small distroless-like with yt-dlp + node for YouTube EJS
FROM python:3.12-slim-bookworm
WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl nodejs \
    && pip install --no-cache-dir "yt-dlp>=2026.7.4" \
    && rm -rf /var/lib/apt/lists/* \
    && node --version && yt-dlp --version

COPY --from=build /out/soop-parser /app/soop-parser

ENV HOST=0.0.0.0 \
    PORT=8080 \
    PLAY_TOKEN_TTL=3600 \
    HTTP_TIMEOUT=45 \
    MAX_SESSIONS=64

# yt-dlp EJS may write cache under HOME
RUN useradd -m -u 10001 appuser
USER appuser
ENV HOME=/home/appuser

EXPOSE 8080
ENTRYPOINT ["/app/soop-parser"]
