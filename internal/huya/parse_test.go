package huya

import (
	"encoding/base64"
	"net/url"
	"testing"
)

func TestParseRoom(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://www.huya.com/lck", "lck"},
		{"https://m.huya.com/123456", "123456"},
		{"huya.com/abc_def", "abc_def"},
	}
	for _, c := range cases {
		got, err := ParseRoom(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %s want %s", c.in, got, c.want)
		}
	}
	if IsURL("lck") || IsURL("12345") {
		t.Fatal("bare id must not match huya without host")
	}
	if IsURL("https://live.bilibili.com/6") {
		t.Fatal("bilibili should not match huya")
	}
	if !IsURL("https://www.huya.com/lck") {
		t.Fatal("expected huya url")
	}
}

func TestExtractJSONObject(t *testing.T) {
	s := `{"a":1,"b":{"c":"x}"},"d":2} trailing`
	obj, ok := extractJSONObject(s)
	if !ok {
		t.Fatal("expected ok")
	}
	if obj != `{"a":1,"b":{"c":"x}"},"d":2}` {
		t.Fatalf("got %s", obj)
	}
}

func TestRebuildAntiCode(t *testing.T) {
	fm := url.QueryEscape(base64.StdEncoding.EncodeToString([]byte("testprefix_xxx")))
	anti := "wsTime=65f00000&fm=" + fm + "&ctype=huya_live&fs=bgct"
	q, err := rebuildAntiCode(anti, "streamname")
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("wsSecret") == "" {
		t.Fatal("missing wsSecret")
	}
	if q.Get("u") == "" || q.Get("uuid") == "" || q.Get("seqid") == "" {
		t.Fatalf("missing uid fields: %v", q)
	}
	// streamget uses large uid ~1.4e12
	if len(q.Get("u")) < 10 {
		t.Fatalf("uid too short: %s", q.Get("u"))
	}
	if q.Get("sv") != "2403051612" {
		t.Fatalf("sv %s", q.Get("sv"))
	}
}

func TestIsReplayTitle(t *testing.T) {
	if !isReplayTitle("【重播】16点直播GEN vs DRX") {
		t.Fatal("重播 head")
	}
	if !isReplayTitle("昨天的比赛回放") {
		t.Fatal("回放 tail")
	}
	if isReplayTitle("正在直播 EWC") {
		t.Fatal("live title")
	}
}

func TestEnsureHTTPS(t *testing.T) {
	if ensureHTTPS("http://a.com/x") != "https://a.com/x" {
		t.Fatal("http upgrade")
	}
}
