package proxy

import "testing"

func TestIsHLSPlaylistURL(t *testing.T) {
	if !IsHLSPlaylistURL("https://example.sooplive.com/a/b.m3u8?x=1") {
		t.Fatal("expected playlist")
	}
	seg := "https://rr5---sn-xx.googlevideo.com/videoplayback/id/abc/playlist/index.m3u8/sq/1/file/seg.ts"
	if IsHLSPlaylistURL(seg) {
		t.Fatal("youtube segment must not be playlist")
	}
}

func TestIsAllowedUpstream(t *testing.T) {
	if !IsAllowedUpstream("https://live-global-cdn-v02.sooplive.com/x.ts", "soop") {
		t.Fatal("soop cdn")
	}
	if IsAllowedUpstream("https://soop.evil.com/x", "soop") {
		t.Fatal("keyword bypass should be gone")
	}
	if IsAllowedUpstream("https://d111111abcdef8.cloudfront.net/x", "soop") {
		t.Fatal("cloudfront should not be open")
	}
	if !IsAllowedUpstream("https://rr1---sn-a.googlevideo.com/videoplayback", "youtube") {
		t.Fatal("googlevideo")
	}
}

func TestRewriteM3U8(t *testing.T) {
	in := `#EXTM3U
#EXTINF:2.0,
segment1.ts
#EXTINF:2.0,
https://cdn.sooplive.com/preloading/x.ts
#EXTINF:2.0,
https://cdn.sooplive.com/real/seg.ts
`
	out := RewriteM3U8(in, "https://cdn.sooplive.com/path/playlist.m3u8", "https://soop.uuun.de/api/hls/tok/proxy?u=")
	if contains(out, "preloading") {
		t.Fatal("preloading not filtered")
	}
	if !contains(out, "https://soop.uuun.de/api/hls/tok/proxy?u=") {
		t.Fatal("missing proxy prefix")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
