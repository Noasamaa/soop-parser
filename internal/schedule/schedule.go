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

// Riot-adjacent esports API (community-documented public key).
const (
	esportsBase  = "https://esports-api.lolesports.com/persisted/gw"
	esportsKey   = "0TvQnueqKa5mxJntVWt0w4LpLfEkrV1Ta8rQBb9Z"
	hideAfterEnd = 72 * time.Hour // whole tournament end + 3 days
	maxBody      = 4 << 20
	maxDoneKeep  = 24 // recent completed (API state is often wrong; we derive completed)
)

var trackedLeagues = []struct {
	ID, Slug, Label string
}{
	{"116838530616006090", "ewc_lol", "EWC 电竞世俱杯"},
	{"98767991325878492", "msi", "MSI"},
	{"98767975604431411", "worlds", "Worlds"},
}

// Team is one side of a match.
type Team struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Image  string `json:"image,omitempty"`
	Wins   int    `json:"wins"`
	Record string `json:"record,omitempty"`
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
		pick := pickVisibleTournament(tours, now)
		if pick == nil {
			continue
		}
		matches, err := s.fetchSchedule(ctx, L.ID, L.Label)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			matches = nil
		}
		filtered := filterAndTrimMatches(matches, pick)
		// skip empty far-future shells
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

func pickVisibleTournament(tours []tournamentMeta, now time.Time) *tournamentMeta {
	var pick *tournamentMeta
	for i := range tours {
		t := &tours[i]
		if now.After(t.End.Add(hideAfterEnd)) {
			continue
		}
		if pick == nil || t.Start.After(pick.Start) {
			pick = t
		}
	}
	return pick
}

func filterAndTrimMatches(matches []Match, pick *tournamentMeta) []Match {
	winStart := pick.Start.Add(-24 * time.Hour)
	winEnd := pick.End.Add(48 * time.Hour)
	var live, soon, done []Match
	for _, m := range matches {
		if m.StartTime.Before(winStart) || m.StartTime.After(winEnd) {
			continue
		}
		switch m.State {
		case "inProgress":
			live = append(live, m)
		case "unstarted":
			soon = append(soon, m)
		default:
			done = append(done, m)
		}
	}
	// trim oldest completed by start time
	if len(done) > maxDoneKeep {
		sort.SliceStable(done, func(i, j int) bool {
			return done[i].StartTime.Before(done[j].StartTime)
		})
		done = done[len(done)-maxDoneKeep:]
	}
	out := make([]Match, 0, len(live)+len(soon)+len(done))
	out = append(out, live...)
	out = append(out, soon...)
	out = append(out, done...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartTime.Before(out[j].StartTime)
	})
	return out
}

func (s *Service) fetchTournaments(ctx context.Context, leagueID string) ([]tournamentMeta, error) {
	u := fmt.Sprintf("%s/getTournamentsForLeague?hl=en-US&leagueId=%s", esportsBase, leagueID)
	var resp struct {
		Data struct {
			Leagues []struct {
				Tournaments []struct {
					ID, Slug, StartDate, EndDate string
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
			end = end.Add(23*time.Hour + 59*time.Minute)
			out = append(out, tournamentMeta{ID: t.ID, Slug: t.Slug, Start: start, End: end})
		}
	}
	return out, nil
}

func (s *Service) fetchSchedule(ctx context.Context, leagueID, leagueLabel string) ([]Match, error) {
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
							Count int `json:"count"`
						} `json:"strategy"`
						Teams []struct {
							Name, Code, Image string
							Result            *struct {
								GameWins int    `json:"gameWins"`
								Outcome  string `json:"outcome"` // win | loss | null
							} `json:"result"`
							Record *struct {
								Wins, Losses int
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
		hasCode := false
		for _, t := range e.Match.Teams {
			if t.Code != "" && t.Code != "TBD" {
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
		var maxWins, totalWins int
		hasOutcome := false
		for _, t := range e.Match.Teams {
			wins := 0
			if t.Result != nil {
				wins = t.Result.GameWins
				o := strings.ToLower(t.Result.Outcome)
				if o == "win" || o == "loss" {
					hasOutcome = true
				}
			}
			if wins > maxWins {
				maxWins = wins
			}
			totalWins += wins
			rec := ""
			if t.Record != nil {
				rec = fmt.Sprintf("%d-%d", t.Record.Wins, t.Record.Losses)
			}
			teams = append(teams, Team{
				Code: t.Code, Name: t.Name, Image: ensureHTTPS(t.Image),
				Wins: wins, Record: rec,
			})
		}
		// Riot often leaves event.state as "unstarted" after results land.
		state := normalizeMatchState(e.State, maxWins, totalWins, hasOutcome, bo)
		out = append(out, Match{
			ID: e.Match.ID, StartTime: st.UTC(), State: state,
			Block: e.BlockName, BO: bo, Teams: teams,
			League: leagueLabel, LeagueID: leagueID,
		})
	}
	return out, nil
}

// normalizeMatchState fixes stale API state using series results.
// Observed: getSchedule returns state=unstarted while result.outcome is win/loss.
func normalizeMatchState(apiState string, maxWins, totalWins int, hasOutcome bool, bo int) string {
	switch apiState {
	case "inProgress", "completed":
		return apiState
	}
	need := bo/2 + 1 // BO1→1, BO3→2, BO5→3
	if need < 1 {
		need = 1
	}
	if maxWins >= need || hasOutcome {
		return "completed"
	}
	if totalWins > 0 {
		return "inProgress"
	}
	if apiState == "" {
		return "unstarted"
	}
	return apiState
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
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
