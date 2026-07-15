package bilibili

import "testing"

func TestParseRoomID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://live.bilibili.com/6", "6"},
		{"https://live.bilibili.com/h5/6", "6"},
		{"https://live.bilibili.com/blanc/123456", "123456"},
		{"live.bilibili.com/999", "999"},
		{"4245963", "4245963"},
	}
	for _, c := range cases {
		got, err := ParseRoomID(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %s want %s", c.in, got, c.want)
		}
	}
	if IsURL("https://www.youtube.com/watch?v=x") {
		t.Fatal("youtube should not match bilibili")
	}
	if IsURL("https://www.huya.com/lck") {
		t.Fatal("huya should not match bilibili")
	}
	if !IsURL("https://live.bilibili.com/6") {
		t.Fatal("expected bilibili url")
	}
}

func TestPickStreamsEmpty(t *testing.T) {
	h, f := pickStreams(nil)
	if h != "" || f != "" {
		t.Fatal("expected empty")
	}
}
