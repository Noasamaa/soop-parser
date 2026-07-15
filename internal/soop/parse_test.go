package soop

import "testing"

func TestParseURL(t *testing.T) {
	ch, bno, err := ParseURL("https://play.sooplive.com/loltw/295590617")
	if err != nil || ch != "loltw" || bno != "295590617" {
		t.Fatalf("got %s %s %v", ch, bno, err)
	}
	ch, bno, err = ParseURL("https://play.sooplive.com/loltw")
	if err != nil || ch != "loltw" || bno != "" {
		t.Fatalf("channel only: %s %s %v", ch, bno, err)
	}
	if _, _, err := ParseURL("https://example.com/x"); err == nil {
		t.Fatal("expected error")
	}
}
