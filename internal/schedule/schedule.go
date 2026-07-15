package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Public Riot-adjacent esports API (community-documented; key is public).
const (
	esportsBase = "https://esports-api.lolesports.com/persisted/gw"
	esportsKey  = "0TvQnueqKa5mxJntVWt0w4LpLfEkrV1Ta8rQBb9Z"
	// hide tournament schedule this long after tournament.endDate
	hideAfterEnd = 72 * time.Hour
)

// Tracked leagues (slug → leagueId).
var trackedLeagues = []struct {
	ID    string
	Slug  string
	Label string
}{
	{ID: "116838530616006090", Slug: "ewc_lol", Label: "EWC 电竞世俱杯"},
	{ID: "98767991325878492", Slug: "msi", Label: "MSI"},
	{ID: "98767975604431411", Slug: "worlds", Label: "Worlds"},
}

// Team is one side of a match.
type Team struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Image  string `json:"image,omitempty"`
	Wins   int    `json:"wins"`
	Record string `json:"record,omitempty"` // e.g. 1-0 series record in stage
}

// Match is one scheduled series.
type Match struct {
	ID        string    `json:"id"`
	StartTime time.Time `json:"start_time"`
	State     string    `json:"state"` // unstarted | inProgress | completed
	Block     string    `json:"block,omitempty"`
	BO        int       `json:"bo"`
	Teams     []Team    `json:"teams"`
	League    string    `json:"league"`
	LeagueID  string    `json:"league_id"`
}

// Tournament is one edition of a league with visibility window.
type Tournament struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Label     string    `json:"label"`
	League    string    `json:"league"`
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	Visible   bool      `json:"visible"`
	HideAfter time.Time `json:"hide_after"`
	Matches   []Match   `json:"matches"`
}

// Result is the schedule API payload.
type Result struct {
	UpdatedAt   time.Time    `json:"updated_at"`
	Tournaments []Tournament `json:"tournaments"`
}

// Service fetches and caches esports schedules.
type Service struct {
	http    *http.Client
	mu      sync.Mutex
	cache   *Result
	cacheAt time.Time
	ttl     time.Duration
}

// New builds a schedule service.
func New(base *http.Client) *Service {
	timeout := 25 * time.Second
	var transport http.RoundTripper = http.DefaultTransport
	if base != nil {
		if base.Transport != nil {
			transport = base.Transport
		}
		if base.Timeout > 0 {
			timeout = base.Timeout
		}
	}
	return &Service{
		http: &http.Client{Transport: transport, Timeout: timeout},
		ttl:  2 * time.Minute,
	}
}

// Get returns cached or fresh schedule (only visible tournaments).
func (s *Service) Get(ctx context.Context) (*Result, error) {
	s.mu.Lock()
	if s.cache != nil && time.Since(s.cacheAt) < s.ttl {
		out := s.cache
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	res, err := s.build(ctx)
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cache != nil {
			return s.cache, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.cache = res
	s.cacheAt = time.Now()
	s.mu.Unlock()
	return res, nil
}

func (s *Service) build(ctx context.Context) (*Result, error) {
	now := time.Now().UTC()
	var out []Tournament
	var firstErr error

	for _, L := range trackedLeagues {
		if ctx.Err() != nil {
			break
		}
		tours, err := s.fetchTournaments(ctx, L.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// pick most recent tournament that is still visible (end+3d)
		var pick *tournamentMeta
		for i := range tours {
			t := &tours[i]
			hideAt := t.End.Add(hideAfterEnd)
			if now.After(hideAt) {
				continue
			}
			// prefer currently active or upcoming; among visible pick latest start
			if pick == nil || t.Start.After(pick.Start) {
				pick = t
			}
		}
		if pick == nil {
			continue
		}
		matches, err := s.fetchSchedule(ctx, L.ID, L.Slug, L.Label)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// still list tournament shell
			matches = nil
		}
		// filter matches roughly within tournament window ±1 day
		var filtered []Match
		winStart := pick.Start.Add(-24 * time.Hour)
		winEnd := pick.End.Add(48 * time.Hour)
		for _, m := range matches {
			if m.StartTime.Before(winStart) || m.StartTime.After(winEnd) {
				continue
			}
			filtered = append(filtered, m)
		}
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].StartTime.Before(filtered[j].StartTime)
		})
		// skip empty far-future shells (e.g. Worlds with no events in window yet)
		inWindow := !now.Before(pick.Start.Add(-24*time.Hour)) && !now.After(pick.End.Add(hideAfterEnd))
		if len(filtered) == 0 && !inWindow {
			continue
		}
		if len(filtered) == 0 && now.Before(pick.Start) {
			continue
		}
		out = append(out, Tournament{
			ID:        pick.ID,
			Slug:      pick.Slug,
			Label:     L.Label,
			League:    L.Slug,
			StartDate: pick.Start,
			EndDate:   pick.End,
			Visible:   true,
			HideAfter: pick.End.Add(hideAfterEnd),
			Matches:   filtered,
		})
	}

	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	// active leagues first (more remaining matches / later end)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].EndDate.After(out[j].EndDate)
	})
	return &Result{UpdatedAt: now, Tournaments: out}, nil
}

type tournamentMeta struct {
	ID    string
	Slug  string
	Start time.Time
	End   time.Time
}

func (s *Service) fetchTournaments(ctx context.Context, leagueID string) ([]tournamentMeta, error) {
	u := fmt.Sprintf("%s/getTournamentsForLeague?hl=en-US&leagueId=%s", esportsBase, leagueID)
	var resp struct {
		Data struct {
			Leagues []struct {
				Tournaments []struct {
					ID        string `json:"id"`
					Slug      string `json:"slug"`
					StartDate string `json:"startDate"`
					EndDate   string `json:"endDate"`
				} `json:"tournaments"`
			} `json:"leagues"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	var out []tournamentMeta
	for _, L := range resp.Data.Leagues {
		for _, t := range L.Tournaments {
			start, err1 := parseDay(t.StartDate)
			end, err2 := parseDay(t.EndDate)
			if err1 != nil || err2 != nil {
				continue
			}
			// end of endDate day UTC
			end = end.Add(23*time.Hour + 59*time.Minute)
			out = append(out, tournamentMeta{ID: t.ID, Slug: t.Slug, Start: start, End: end})
		}
	}
	return out, nil
}

func (s *Service) fetchSchedule(ctx context.Context, leagueID, leagueSlug, leagueLabel string) ([]Match, error) {
	u := fmt.Sprintf("%s/getSchedule?hl=en-US&leagueId=%s", esportsBase, leagueID)
	var resp struct {
		Data struct {
			Schedule struct {
				Events []struct {
					StartTime string `json:"startTime"`
					State     string `json:"state"`
					BlockName string `json:"blockName"`
					Type      string `json:"type"`
					Match     *struct {
						ID       string `json:"id"`
						Strategy struct {
							Type  string `json:"type"`
							Count int    `json:"count"`
						} `json:"strategy"`
						Teams []struct {
							Name   string `json:"name"`
							Code   string `json:"code"`
							Image  string `json:"image"`
							Result *struct {
								GameWins int    `json:"gameWins"`
								Outcome  string `json:"outcome"`
							} `json:"result"`
							Record *struct {
								Wins   int `json:"wins"`
								Losses int `json:"losses"`
							} `json:"record"`
						} `json:"teams"`
					} `json:"match"`
				} `json:"events"`
			} `json:"schedule"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range resp.Data.Schedule.Events {
		if e.Type != "" && e.Type != "match" {
			continue
		}
		if e.Match == nil || len(e.Match.Teams) == 0 {
			continue
		}
		// skip placeholder events without team codes
		hasCode := false
		for _, t := range e.Match.Teams {
			if t.Code != "" {
				hasCode = true
				break
			}
		}
		if !hasCode {
			continue
		}
		st, err := time.Parse(time.RFC3339, e.StartTime)
		if err != nil {
			st, err = time.Parse(time.RFC3339Nano, e.StartTime)
			if err != nil {
				continue
			}
		}
		bo := e.Match.Strategy.Count
		if bo <= 0 {
			bo = 1
		}
		teams := make([]Team, 0, len(e.Match.Teams))
		for _, t := range e.Match.Teams {
			wins := 0
			if t.Result != nil {
				wins = t.Result.GameWins
			}
			rec := ""
			if t.Record != nil {
				rec = fmt.Sprintf("%d-%d", t.Record.Wins, t.Record.Losses)
			}
			teams = append(teams, Team{
				Code:   t.Code,
				Name:   t.Name,
				Image:  ensureHTTPS(t.Image),
				Wins:   wins,
				Record: rec,
			})
		}
		out = append(out, Match{
			ID:        e.Match.ID,
			StartTime: st.UTC(),
			State:     e.State,
			Block:     e.BlockName,
			BO:        bo,
			Teams:     teams,
			League:    leagueLabel,
			LeagueID:  leagueID,
		})
	}
	return out, nil
}

func (s *Service) getJSON(ctx context.Context, rawURL string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", esportsKey)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("esports API HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, dest)
}

func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}
	// "2026-07-14" or RFC3339
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("bad date %q", s)
}

func ensureHTTPS(u string) string {
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "http://") {
		return "https://" + strings.TrimPrefix(u, "http://")
	}
	return u
}
