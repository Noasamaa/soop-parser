FROM python:3.12-slim

WORKDIR /app

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOST=0.0.0.0 \
    PORT=8080

# Node is required by yt-dlp for YouTube JS challenges (EJS)
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && node --version

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

EXPOSE 8080

CMD ["sh", "-c", "uvicorn app.main:app --host ${HOST:-0.0.0.0} --port ${PORT:-8080}"]
