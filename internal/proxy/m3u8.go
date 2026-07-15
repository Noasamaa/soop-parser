package proxy

import (
	"net/url"
	"strings"
)

// RewriteM3U8 rewrites media and URI= attributes to go through proxyBase prefix.
// proxyBase must already end with "u=" (caller percent-encodes appended URL).
func RewriteM3U8(content, baseURL, proxyBase string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			out = append(out, raw)
			continue
		}
		if strings.HasPrefix(line, "#") {
			out = append(out, rewriteURIAttr(line, base, proxyBase))
			continue
		}
		abs := resolveURL(base, line)
		if strings.Contains(abs, "preloading") {
			continue
		}
		out = append(out, proxyBase+url.QueryEscape(abs))
	}
	return strings.Join(out, "\n") + "\n"
}

func rewriteURIAttr(line string, base *url.URL, proxyBase string) string {
	// Minimal URI="..." rewriter for EXT-X-KEY / EXT-X-MAP
	const key = `URI="`
	idx := strings.Index(line, key)
	if idx < 0 {
		return line
	}
	start := idx + len(key)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return line
	}
	uri := line[start : start+end]
	if strings.HasPrefix(uri, "data:") {
		return line
	}
	abs := resolveURL(base, uri)
	if strings.Contains(abs, "preloading") {
		return line[:start] + line[start+end:]
	}
	return line[:start] + proxyBase + url.QueryEscape(abs) + line[start+end:]
}

func resolveURL(base *url.URL, ref string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// IsHLSPlaylistURL distinguishes playlists from media segments.
func IsHLSPlaylistURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	if strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".m4s") ||
		strings.HasSuffix(path, ".mp4") || strings.HasSuffix(path, ".m4v") ||
		strings.HasSuffix(path, ".cmfv") || strings.HasSuffix(path, ".cmfa") ||
		strings.HasSuffix(path, ".aac") || strings.HasSuffix(path, ".vtt") {
		return false
	}
	if strings.Contains(path, "/file/seg.ts") || strings.HasSuffix(path, "seg.ts") {
		return false
	}
	if strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, "/manifest") || strings.HasSuffix(path, ".mpd") {
		return true
	}
	if strings.Contains(path, "m3u8") {
		return true
	}
	if strings.Contains(path, "manifest/hls") {
		return true
	}
	return false
}

// GuessMediaType returns a Content-Type for a media URL path.
func GuessMediaType(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "application/octet-stream"
	}
	path := strings.ToLower(u.Path)
	switch {
	case strings.HasSuffix(path, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(path, ".m4s"), strings.HasSuffix(path, ".mp4"), strings.HasSuffix(path, ".m4v"):
		return "video/mp4"
	case strings.HasSuffix(path, ".ts"):
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}
