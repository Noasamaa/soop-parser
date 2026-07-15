package hupu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxBody      = 1 << 20
	maxWalkDepth = 12
	reqTimeout   = 4 * time.Second // fail fast; overseas often dead
)

// Service scrapes Hupu esports ratings + hot comments (best-effort).
type Service struct {
	http    *http.Client
	mu      sync.Mutex
	cache   map[string]cacheEntry
	listTTL time.Duration
	itemTTL time.Duration
}

type cacheEntry struct {
	deadline time.Time
	data     any
}

// PlayerScore is one player's community rating.
type PlayerScore struct {
	Name  string  `json:"name"`
	Team  string  `json:"team,omitempty"`
	Score float64 `json:"score"`
	Count int     `json:"count,omitempty"`
}

// Comment is a high-light comment.
type Comment struct {
	User    string `json:"user"`
	Content string `json:"content"`
	Lights  int    `json:"lights"`
}

// MatchRating is ratings + comments for one match.
type MatchRating struct {
	MatchID   string        `json:"match_id,omitempty"`
	Title     string        `json:"title"`
	Home      string        `json:"home,omitempty"`
	Away      string        `json:"away,omitempty"`
	Players   []PlayerScore `json:"players,omitempty"`
	Comments  []Comment     `json:"comments,omitempty"`
	SourceURL string        `json:"source_url,omitempty"`
	Available bool          `json:"available"`
	Message   string        `json:"message,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type listItem struct {
	MatchID string
	Title   string
	Home    string
	Away    string
}

// New builds a Hupu client.
func New(base *http.Client) *Service {
	timeout := 12 * time.Second
	var transport http.RoundTripper = http.DefaultTransport
	if base != nil {
		if base.Transport != nil {
			transport = base.Transport
		}
		if base.Timeout > 0 && base.Timeout < timeout {
			timeout = base.Timeout
		}
	}
	return &Service{
		http:    &http.Client{Transport: transport, Timeout: timeout},
		cache:   map[string]cacheEntry{},
		listTTL: 3 * time.Minute,
		itemTTL: 5 * time.Minute,
	}
}

// RatingForTeams finds a Hupu match by team codes and returns scores + top comments.
func (s *Service) RatingForTeams(ctx context.Context, teamA, teamB string) (*MatchRating, error) {
	teamA, teamB = strings.TrimSpace(teamA), strings.TrimSpace(teamB)
	key := "teams:" + strings.ToLower(teamA) + ":" + strings.ToLower(teamB)
	if v, ok := s.getCache(key); ok {
		return v.(*MatchRating), nil
	}

	if list, err := s.fetchMatchList(ctx); err == nil {
		for _, it := range list {
			if !teamsMatch(it.Home, it.Away, teamA, teamB) {
				continue
			}
			r, err := s.RatingByMatchID(ctx, it.MatchID)
			if err != nil || r == nil {
				continue
			}
			if r.Title == "" {
				r.Title = it.Title
			}
			if r.Home == "" {
				r.Home = it.Home
			}
			if r.Away == "" {
				r.Away = it.Away
			}
			s.setCache(key, r, s.itemTTL)
			return r, nil
		}
	}

	out := unavailable(teamA, teamB)
	s.setCache(key, out, time.Minute)
	return out, nil
}

// RatingByMatchID fetches one match rating.
func (s *Service) RatingByMatchID(ctx context.Context, matchID string) (*MatchRating, error) {
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return nil, fmt.Errorf("empty match id")
	}
	key := "id:" + matchID
	if v, ok := s.getCache(key); ok {
		return v.(*MatchRating), nil
	}
	r, err := s.fetchMatchDetail(ctx, matchID)
	if err != nil {
		return nil, err
	}
	s.setCache(key, r, s.itemTTL)
	return r, nil
}

func unavailable(a, b string) *MatchRating {
	return &MatchRating{
		Title:     a + " vs " + b,
		Home:      a,
		Away:      b,
		Available: false,
		Message:   "虎扑评分接口暂不可用（海外 IP / 签名校验常见失败）。可打开链接在 App 查看。",
		SourceURL: hupuSearchURL(a, b),
		UpdatedAt: time.Now().UTC(),
	}
}

func (s *Service) fetchMatchList(ctx context.Context) ([]listItem, error) {
	if v, ok := s.getCache("list"); ok {
		return v.([]listItem), nil
	}
	// few paths, short timeout each
	paths := []string{
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/recentMatchList?competitionType=lol",
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/matchList?competitionType=lol&page=1",
	}
	var last error
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			last = err
			continue
		}
		items := parseMatchListJSON(raw)
		if len(items) > 0 {
			s.setCache("list", items, s.listTTL)
			return items, nil
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, fmt.Errorf("hupu: empty match list")
}

func (s *Service) fetchMatchDetail(ctx context.Context, matchID string) (*MatchRating, error) {
	paths := []string{
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/matchDetail?matchId=" + url.QueryEscape(matchID),
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/getMatchScoreInfo?matchId=" + url.QueryEscape(matchID),
	}
	var last error
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			last = err
			continue
		}
		r := parseMatchDetailJSON(raw, matchID)
		if !r.Available {
			continue
		}
		if cs := s.fetchTopComments(ctx, matchID); len(cs) > 0 {
			r.Comments = cs
		}
		return r, nil
	}
	if last != nil {
		return nil, last
	}
	return &MatchRating{
		MatchID: matchID, Available: false, Message: "未解析到评分数据",
		SourceURL: "https://m.hupu.com/", UpdatedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) fetchTopComments(ctx context.Context, matchID string) []Comment {
	paths := []string{
		"https://games.mobileapi.hupu.com/1/8.0.61/bplcommentapi/bpl/score_reply_list?matchId=" + url.QueryEscape(matchID) + "&page=1",
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/getHotComment?matchId=" + url.QueryEscape(matchID),
	}
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			continue
		}
		cs := parseCommentsJSON(raw)
		if len(cs) == 0 {
			continue
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Lights > cs[j].Lights })
		if len(cs) > 3 {
			cs = cs[:3]
		}
		return cs
	}
	return nil
}

func (s *Service) getBytes(ctx context.Context, rawURL string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kanqiu/8.0.61/8333 (iPhone; iOS 16.0; Scale/3.00)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://m.hupu.com/")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	var probe struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Status != 0 && probe.Status != 200 {
		return nil, fmt.Errorf("hupu status=%d %s", probe.Status, probe.Msg)
	}
	return body, nil
}

func parseMatchListJSON(raw []byte) []listItem {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	var items []listItem
	walkJSON(root, 0, func(m map[string]any) {
		// require home+away style fields to avoid random id/name nodes
		home := strField(m, "homeName", "home_name", "homeTeam", "leftName")
		away := strField(m, "awayName", "away_name", "awayTeam", "rightName")
		id := strField(m, "matchId", "match_id")
		if id == "" || home == "" || away == "" {
			return
		}
		title := strField(m, "title", "matchTitle")
		if title == "" {
			title = home + " vs " + away
		}
		items = append(items, listItem{MatchID: id, Title: title, Home: home, Away: away})
	})
	seen := map[string]bool{}
	var out []listItem
	for _, it := range items {
		if seen[it.MatchID] {
			continue
		}
		seen[it.MatchID] = true
		out = append(out, it)
	}
	return out
}

func parseMatchDetailJSON(raw []byte, matchID string) *MatchRating {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return &MatchRating{MatchID: matchID, Available: false, UpdatedAt: time.Now().UTC()}
	}
	var players []PlayerScore
	walkJSON(root, 0, func(m map[string]any) {
		name := strField(m, "playerName", "player_name", "personName")
		score := floatField(m, "score", "avgScore", "averageScore", "rating")
		if name == "" || score < 1 || score > 10.5 {
			return
		}
		players = append(players, PlayerScore{
			Name:  name,
			Team:  strField(m, "teamName", "team_name"),
			Score: score,
			Count: int(floatField(m, "scoreCount", "ratingCount", "count")),
		})
	})
	byName := map[string]PlayerScore{}
	for _, p := range players {
		if old, ok := byName[p.Name]; !ok || p.Score > old.Score {
			byName[p.Name] = p
		}
	}
	players = players[:0]
	for _, p := range byName {
		players = append(players, p)
	}
	sort.SliceStable(players, func(i, j int) bool { return players[i].Score > players[j].Score })
	if len(players) > 12 {
		players = players[:12]
	}

	title, home, away := "", "", ""
	walkJSON(root, 0, func(m map[string]any) {
		if title == "" {
			title = strField(m, "title", "matchTitle", "match_name")
		}
		if home == "" {
			home = strField(m, "homeName", "home_name", "homeTeam")
		}
		if away == "" {
			away = strField(m, "awayName", "away_name", "awayTeam")
		}
	})

	ok := len(players) > 0
	msg := ""
	if !ok {
		msg = "无选手评分字段"
	}
	return &MatchRating{
		MatchID: matchID, Title: title, Home: home, Away: away,
		Players: players, Available: ok, Message: msg,
		SourceURL: "https://m.hupu.com/", UpdatedAt: time.Now().UTC(),
	}
}

func parseCommentsJSON(raw []byte) []Comment {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	var cs []Comment
	walkJSON(root, 0, func(m map[string]any) {
		content := strField(m, "content", "quote")
		if content == "" || len([]rune(content)) < 4 {
			return
		}
		// require light-like field to reduce false positives
		lights := int(floatField(m, "light", "lights", "light_count", "lightNum", "lightCount"))
		user := strField(m, "userName", "username", "user_name", "nick", "nickname")
		if user == "" && lights == 0 {
			return
		}
		cs = append(cs, Comment{User: user, Content: content, Lights: lights})
	})
	seen := map[string]bool{}
	var out []Comment
	for _, c := range cs {
		if seen[c.Content] {
			continue
		}
		seen[c.Content] = true
		out = append(out, c)
	}
	return out
}

func walkJSON(v any, depth int, fn func(map[string]any)) {
	if depth > maxWalkDepth {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, child := range t {
			walkJSON(child, depth+1, fn)
		}
	case []any:
		// cap array fan-out
		n := len(t)
		if n > 200 {
			n = 200
		}
		for i := 0; i < n; i++ {
			walkJSON(t[i], depth+1, fn)
		}
	}
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case float64:
			return fmt.Sprintf("%.0f", t)
		}
	}
	return ""
}

func floatField(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return t
		case string:
			var f float64
			_, _ = fmt.Sscanf(t, "%f", &f)
			return f
		}
	}
	return 0
}

func teamsMatch(home, away, a, b string) bool {
	h, aw := normTeam(home), normTeam(away)
	x, y := normTeam(a), normTeam(b)
	if h == "" || aw == "" {
		return false
	}
	return (containsTeam(h, x) && containsTeam(aw, y)) || (containsTeam(h, y) && containsTeam(aw, x))
}

func normTeam(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "")
}

func containsTeam(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.Contains(a, b) || strings.Contains(b, a)
}

func hupuSearchURL(a, b string) string {
	q := url.QueryEscape(strings.TrimSpace(a + " vs " + b + " 评分"))
	return "https://m.hupu.com/bbs/search?q=" + q
}

func (s *Service) getCache(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.deadline) {
		delete(s.cache, key)
		return nil, false
	}
	return e.data, true
}

func (s *Service) setCache(key string, data any, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) > 128 {
		s.cache = map[string]cacheEntry{}
	}
	s.cache[key] = cacheEntry{deadline: time.Now().Add(ttl), data: data}
}
