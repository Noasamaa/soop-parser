package schedule

import (
	"testing"
	"time"
)

func TestNormalizeMatchState(t *testing.T) {
	cases := []struct {
		name               string
		api                string
		maxWins, total, bo int
		hasOutcome         bool
		want               string
	}{
		{"trust completed", "completed", 0, 0, 1, false, "completed"},
		{"trust inProgress", "inProgress", 0, 0, 1, false, "inProgress"},
		// Riot bug: unstarted but BO1 already decided
		{"bo1 via wins", "unstarted", 1, 1, 1, false, "completed"},
		{"bo1 via outcome", "unstarted", 0, 0, 1, true, "completed"},
		{"bo3 mid series", "unstarted", 1, 1, 3, false, "inProgress"},
		{"bo3 finished", "unstarted", 2, 3, 3, false, "completed"},
		{"not started", "unstarted", 0, 0, 1, false, "unstarted"},
		{"empty api", "", 0, 0, 1, false, "unstarted"},
	}
	for _, c := range cases {
		got := normalizeMatchState(c.api, c.maxWins, c.total, c.hasOutcome, c.bo)
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestFilterAndTrimMatchesKeepsRecentDone(t *testing.T) {
	pick := &tournamentMeta{
		Start: mustDay("2026-07-01"),
		End:   mustDay("2026-07-31"),
	}

	var matches []Match
	for i := 0; i < 30; i++ {
		matches = append(matches, Match{
			ID:        "c",
			StartTime: mustDay("2026-07-01").AddDate(0, 0, i),
			State:     "completed",
			Teams:     []Team{{Code: "A", Wins: 1}, {Code: "B", Wins: 0}},
		})
	}
	matches = append(matches,
		Match{ID: "live", StartTime: mustDay("2026-07-20"), State: "inProgress", Teams: []Team{{Code: "X"}, {Code: "Y"}}},
		Match{ID: "soon", StartTime: mustDay("2026-07-21"), State: "unstarted", Teams: []Team{{Code: "P"}, {Code: "Q"}}},
	)

	out := filterAndTrimMatches(matches, pick)
	var done, live, soon int
	var lastCompleted time.Time
	for _, m := range out {
		switch m.State {
		case "completed":
			done++
			lastCompleted = m.StartTime
		case "inProgress":
			live++
		case "unstarted":
			soon++
		}
	}
	if done != maxDoneKeep {
		t.Fatalf("done=%d want %d", done, maxDoneKeep)
	}
	if live != 1 || soon != 1 {
		t.Fatalf("live=%d soon=%d", live, soon)
	}
	wantLast := mustDay("2026-07-01").AddDate(0, 0, 29)
	if !lastCompleted.Equal(wantLast) {
		t.Fatalf("expected latest completed %v, got %v", wantLast, lastCompleted)
	}
}

func mustDay(s string) time.Time {
	t, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return t
}
