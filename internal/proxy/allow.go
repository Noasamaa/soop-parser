package proxy

import (
	"net"
	"net/url"
	"strings"
)

// Platform host allowlists (no global cloudfront/akamai, no keyword matching).
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

func suffixesFor(platform string) []string {
	if platform == "youtube" {
		return youtubeSuffixes
	}
	return soopSuffixes
}

// MatchingSuffix returns the allowlisted suffix that host matches, or "".
func MatchingSuffix(host, platform string) string {
	host = strings.ToLower(host)
	for _, s := range suffixesFor(platform) {
		if host == s || strings.HasSuffix(host, "."+s) {
			return s
		}
	}
	return ""
}

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
		return false
	}
	return MatchingSuffix(host, platform) != ""
}

// AllowedForSession restricts proxy targets to the same CDN family as the session.
// sessionHost is the hostname of the resolved upstream (playlist/media root).
func AllowedForSession(raw, platform, sessionHost string) bool {
	if !IsAllowedUpstream(raw, platform) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	sessionHost = strings.ToLower(sessionHost)
	if sessionHost == "" {
		return true
	}
	if host == sessionHost {
		return true
	}
	a := MatchingSuffix(sessionHost, platform)
	b := MatchingSuffix(host, platform)
	return a != "" && a == b
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
