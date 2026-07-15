package proxy

import (
	"net"
	"net/url"
	"strings"
)

// Platform host allowlists (tightened — no global cloudfront/akamai).
var (
	soopSuffixes = []string{
		"sooplive.com",
		"sooplive.co.kr",
		"afreecatv.com",
		"afreeca.tv",
		"afcdn.net",
		"soocdn.com",
	}
	youtubeSuffixes = []string{
		"youtube.com",
		"youtu.be",
		"googlevideo.com",
		"ytimg.com",
		"ggpht.com",
		"googleusercontent.com",
		"gvt1.com",
	}
)

// IsAllowedUpstream reports whether url may be fetched for the given platform.
func IsAllowedUpstream(raw, platform string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil {
		// Block raw IP SSRF targets
		return false
	}
	suffixes := soopSuffixes
	if platform == "youtube" {
		suffixes = youtubeSuffixes
	}
	for _, s := range suffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// UpstreamHeaders returns Referer/Origin for the platform.
func UpstreamHeaders(platform, channel string) map[string]string {
	if platform == "youtube" {
		return map[string]string{
			"Referer":    "https://www.youtube.com/",
			"Origin":     "https://www.youtube.com",
			"User-Agent": defaultUA,
		}
	}
	ref := "https://play.sooplive.com/"
	if channel != "" {
		ref = "https://play.sooplive.com/" + channel
	}
	return map[string]string{
		"Referer":    ref,
		"Origin":     "https://play.sooplive.com",
		"User-Agent": defaultUA,
	}
}

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// DefaultUA is the shared browser UA string.
func DefaultUA() string { return defaultUA }
