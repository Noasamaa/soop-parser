package proxy

import (
	"strings"
	"testing"
)

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
		t.Fatal("evil host")
	}
	if IsAllowedUpstream("https://d111111abcdef8.cloudfront.net/x", "soop") {
		t.Fatal("cloudfront should not be open")
	}
	if !IsAllowedUpstream("https://rr1---sn-a.googlevideo.com/videoplayback", "youtube") {
		t.Fatal("googlevideo")
	}
}

func TestAllowedForSession(t *testing.T) {
	sess := "live-global-cdn-v02.sooplive.com"
	ok := "https://live-global-cdn-v02.sooplive.com/seg.ts"
	if !AllowedForSession(ok, "soop", sess) {
		t.Fatal("same host")
	}
	ok2 := "https://other-edge.sooplive.com/seg.ts"
	if !AllowedForSession(ok2, "soop", sess) {
		t.Fatal("same sooplive.com family")
	}
	// different allowlisted family under soop platform
	if AllowedForSession("https://cdn.afreecatv.com/x", "soop", sess) {
		t.Fatal("should not jump afreeca when session is sooplive")
	}
	if AllowedForSession("https://rr1.googlevideo.com/x", "soop", sess) {
		t.Fatal("youtube host with soop session")
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
	if strings.Contains(out, "preloading") {
		t.Fatal("preloading not filtered")
	}
	if !strings.Contains(out, "https://soop.uuun.de/api/hls/tok/proxy?u=") {
		t.Fatal("missing proxy prefix")
	}
}
