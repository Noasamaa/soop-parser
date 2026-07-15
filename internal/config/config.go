package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds process configuration from environment variables.
type Config struct {
	AccessToken        string
	PublicBaseURL      string
	SOOPUsername       string
	SOOPPassword       string
	YouTubeCookiesFile string
	PlayTokenTTL       time.Duration
	HTTPTimeout        time.Duration
	Host               string
	Port               string
	MaxSessions        int
	MaxUpstreamConns   int
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Load reads configuration from the environment.
func Load() Config {
	ttlSec := getenvInt("PLAY_TOKEN_TTL", 3600)
	timeoutSec := getenvInt("HTTP_TIMEOUT", 45)
	return Config{
		AccessToken:        strings.TrimSpace(os.Getenv("ACCESS_TOKEN")),
		PublicBaseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/"),
		SOOPUsername:       strings.TrimSpace(os.Getenv("SOOP_USERNAME")),
		SOOPPassword:       strings.TrimSpace(os.Getenv("SOOP_PASSWORD")),
		YouTubeCookiesFile: strings.TrimSpace(os.Getenv("YOUTUBE_COOKIES_FILE")),
		PlayTokenTTL:       time.Duration(ttlSec) * time.Second,
		HTTPTimeout:        time.Duration(timeoutSec) * time.Second,
		Host:               getenv("HOST", "0.0.0.0"),
		Port:               getenv("PORT", "8080"),
		MaxSessions:        getenvInt("MAX_SESSIONS", 64),
		MaxUpstreamConns:   getenvInt("MAX_UPSTREAM_CONNS", 64),
	}
}

func (c Config) Addr() string {
	return c.Host + ":" + c.Port
}
