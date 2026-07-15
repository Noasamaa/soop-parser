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
		{"lck", "lck"},
		{"12345", "12345"},
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

func TestBuildStreamParams(t *testing.T) {
	fm := url.QueryEscape(base64.StdEncoding.EncodeToString([]byte("testprefix_xxx")))
	anti := "wsTime=65f00000&fm=" + fm + "&ctype=huya_live&fs=fsval"
	q, err := buildStreamParams(anti, "streamname", 0)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("wsSecret") == "" || q.Get("wsTime") != "65f00000" {
		t.Fatalf("bad params: %v", q)
	}
	if q.Get("ratio") != "0" {
		t.Fatal("ratio")
	}
}
