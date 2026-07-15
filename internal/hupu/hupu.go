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

// Service scrapes Hupu esports ratings + hot comments (best-effort).
// Overseas IPs often get status=500 from mobile APIs; we degrade gracefully.
type Service struct {
	http    *http.Client
	mu      sync.Mutex
	cache   map[string]cacheEntry
	listTTL time.Duration
	itemTTL time.Duration
}

type cacheEntry struct {
	at   time.Time
	data any
}

// PlayerScore is one player's community rating.
type PlayerScore struct {
	Name   string  `json:"name"`
	Team   string  `json:"team,omitempty"`
	Score  float64 `json:"score"`
	Count  int     `json:"count,omitempty"` // number of ratings
	Avatar string  `json:"avatar,omitempty"`
}

// Comment is a hot (high-light) comment.
type Comment struct {
	User    string `json:"user"`
	Content string `json:"content"`
	Lights  int    `json:"lights"` // 点赞/亮了
}

// MatchRating is ratings + comments for one match.
type MatchRating struct {
	MatchID   string        `json:"match_id,omitempty"`
	Title     string        `json:"title"`
	Home      string        `json:"home,omitempty"`
	Away      string        `json:"away,omitempty"`
	Players   []PlayerScore `json:"players,omitempty"`
	Comments  []Comment     `json:"comments,omitempty"` // top 3 by lights
	SourceURL string        `json:"source_url,omitempty"`
	Available bool          `json:"available"`
	Message   string        `json:"message,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// ListItem is a recent scoreable match on Hupu.
type ListItem struct {
	MatchID   string    `json:"match_id"`
	Title     string    `json:"title"`
	Home      string    `json:"home"`
	Away      string    `json:"away"`
	StartTime time.Time `json:"start_time,omitempty"`
	Status    string    `json:"status,omitempty"`
}

// New builds a Hupu client.
func New(base *http.Client) *Service {
	timeout := 15 * time.Second
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
		http:    &http.Client{Transport: transport, Timeout: timeout},
		cache:   map[string]cacheEntry{},
		listTTL: 3 * time.Minute,
		itemTTL: 5 * time.Minute,
	}
}

// ListRecent tries to list recent LOL score matches.
func (s *Service) ListRecent(ctx context.Context) ([]ListItem, error) {
	if v, ok := s.getCache("list"); ok {
		return v.([]ListItem), nil
	}
	items, err := s.fetchMatchList(ctx)
	if err != nil {
		return nil, err
	}
	s.setCache("list", items, s.listTTL)
	return items, nil
}

// RatingForTeams finds a Hupu match by team codes/names and returns scores + top comments.
func (s *Service) RatingForTeams(ctx context.Context, teamA, teamB string) (*MatchRating, error) {
	key := "teams:" + strings.ToLower(teamA) + ":" + strings.ToLower(teamB)
	if v, ok := s.getCache(key); ok {
		return v.(*MatchRating), nil
	}

	// 1) try match list then detail
	list, err := s.ListRecent(ctx)
	if err == nil {
		for _, it := range list {
			if teamsMatch(it.Home, it.Away, teamA, teamB) {
				r, err := s.RatingByMatchID(ctx, it.MatchID)
				if err == nil && r != nil {
					if r.Title == "" {
						r.Title = it.Title
					}
					r.Home = it.Home
					r.Away = it.Away
					s.setCache(key, r, s.itemTTL)
					return r, nil
				}
			}
		}
	}

	// 2) try search-style detail endpoints with team names
	r, err := s.fetchByTeamQuery(ctx, teamA, teamB)
	if err == nil && r != nil && r.Available {
		s.setCache(key, r, s.itemTTL)
		return r, nil
	}

	// degrade
	out := &MatchRating{
		Title:     fmt.Sprintf("%s vs %s", teamA, teamB),
		Home:      teamA,
		Away:      teamB,
		Available: false,
		Message:   "虎扑评分接口暂不可用（常见于海外 IP 或签名校验）。可打开下方链接在 App/网页查看。",
		SourceURL: hupuSearchURL(teamA, teamB),
		UpdatedAt: time.Now().UTC(),
	}
	s.setCache(key, out, 1*time.Minute)
	return out, nil
}

// RatingByMatchID fetches one match rating page/API.
func (s *Service) RatingByMatchID(ctx context.Context, matchID string) (*MatchRating, error) {
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

func (s *Service) fetchMatchList(ctx context.Context) ([]ListItem, error) {
	// try several known mobile paths
	paths := []string{
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/recentMatchList?competitionType=lol",
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/homeMatchList?competitionType=lol",
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/matchList?competitionType=lol&page=1",
		"https://games.mobileapi.hupu.com/3/8.0.61/bplapi/en/score/recentMatchList",
		"https://games.mobileapi.hupu.com/1/7.5.60/bplapi/en/rating/matchList?competitionType=lol",
	}
	var last error
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			last = err
			continue
		}
		items, err := parseMatchListJSON(raw)
		if err != nil {
			last = err
			continue
		}
		if len(items) > 0 {
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
		"https://games.mobileapi.hupu.com/3/8.0.61/bplapi/en/score/matchDetail?matchId=" + url.QueryEscape(matchID),
	}
	var last error
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			last = err
			continue
		}
		r, err := parseMatchDetailJSON(raw, matchID)
		if err != nil {
			last = err
			continue
		}
		if r.Available {
			// comments
			if comments, err := s.fetchTopComments(ctx, matchID); err == nil && len(comments) > 0 {
				r.Comments = comments
			}
			return r, nil
		}
	}
	if last != nil {
		return nil, last
	}
	return &MatchRating{
		MatchID:   matchID,
		Available: false,
		Message:   "未解析到评分数据",
		SourceURL: "https://m.hupu.com/",
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) fetchByTeamQuery(ctx context.Context, a, b string) (*MatchRating, error) {
	// some builds accept keyword search
	q := url.QueryEscape(a + " " + b)
	paths := []string{
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/search?keyword=" + q,
	}
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			continue
		}
		items, err := parseMatchListJSON(raw)
		if err != nil || len(items) == 0 {
			continue
		}
		return s.fetchMatchDetail(ctx, items[0].MatchID)
	}
	return nil, fmt.Errorf("no team query result")
}

func (s *Service) fetchTopComments(ctx context.Context, matchID string) ([]Comment, error) {
	paths := []string{
		"https://games.mobileapi.hupu.com/1/8.0.61/bplcommentapi/bpl/score_reply_list?matchId=" + url.QueryEscape(matchID) + "&page=1",
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/commentList?matchId=" + url.QueryEscape(matchID),
		"https://games.mobileapi.hupu.com/1/8.0.61/bplapi/en/score/getHotComment?matchId=" + url.QueryEscape(matchID),
	}
	for _, u := range paths {
		raw, err := s.getBytes(ctx, u)
		if err != nil {
			continue
		}
		cs, err := parseCommentsJSON(raw)
		if err != nil || len(cs) == 0 {
			continue
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Lights > cs[j].Lights })
		if len(cs) > 3 {
			cs = cs[:3]
		}
		return cs, nil
	}
	return nil, fmt.Errorf("no comments")
}

func (s *Service) getBytes(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	// Hupu often returns 200 with status:500 body
	var probe struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Status != 0 && probe.Status != 200 {
		return nil, fmt.Errorf("hupu status=%d %s", probe.Status, probe.Msg)
	}
	return body, nil
}

func parseMatchListJSON(raw []byte) ([]ListItem, error) {
	// flexible walk for common shapes
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var items []ListItem
	walkJSON(root, func(m map[string]any) {
		id := strField(m, "matchId", "match_id", "id", "gid")
		home := strField(m, "homeName", "home_name", "homeTeam", "home", "teamA", "leftName")
		away := strField(m, "awayName", "away_name", "awayTeam", "away", "teamB", "rightName")
		title := strField(m, "title", "matchTitle", "name")
		if id == "" || (home == "" && away == "" && title == "") {
			return
		}
		if title == "" {
			title = strings.TrimSpace(home + " vs " + away)
		}
		items = append(items, ListItem{
			MatchID: id,
			Title:   title,
			Home:    home,
			Away:    away,
			Status:  strField(m, "status", "matchStatus", "state"),
		})
	})
	// dedupe
	seen := map[string]bool{}
	var out []ListItem
	for _, it := range items {
		if seen[it.MatchID] {
			continue
		}
		seen[it.MatchID] = true
		out = append(out, it)
	}
	return out, nil
}

func parseMatchDetailJSON(raw []byte, matchID string) (*MatchRating, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var players []PlayerScore
	walkJSON(root, func(m map[string]any) {
		name := strField(m, "playerName", "player_name", "name", "nickname", "personName")
		score := floatField(m, "score", "rating", "avgScore", "averageScore", "grade")
		if name == "" || score <= 0 {
			return
		}
		// heuristic: score pages use 2-10
		if score < 1 || score > 10.5 {
			return
		}
		players = append(players, PlayerScore{
			Name:   name,
			Team:   strField(m, "teamName", "team", "team_name"),
			Score:  score,
			Count:  int(floatField(m, "count", "scoreCount", "num", "ratingCount")),
			Avatar: strField(m, "avatar", "head", "logo", "playerLogo"),
		})
	})
	// dedupe by name keep higher score
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

	title := ""
	home, away := "", ""
	walkJSON(root, func(m map[string]any) {
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
	return &MatchRating{
		MatchID:   matchID,
		Title:     title,
		Home:      home,
		Away:      away,
		Players:   players,
		Available: ok,
		Message:   ifelse(ok, "", "无选手评分字段"),
		SourceURL: "https://m.hupu.com/",
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func parseCommentsJSON(raw []byte) ([]Comment, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var cs []Comment
	walkJSON(root, func(m map[string]any) {
		content := strField(m, "content", "quote", "text", "body", "reply")
		if content == "" || len([]rune(content)) < 2 {
			return
		}
		user := strField(m, "userName", "username", "user_name", "nick", "nickname", "author")
		lights := int(floatField(m, "light", "lights", "light_count", "lightNum", "like", "likes", "recommend"))
		cs = append(cs, Comment{User: user, Content: content, Lights: lights})
	})
	// dedupe content
	seen := map[string]bool{}
	var out []Comment
	for _, c := range cs {
		k := c.Content
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out, nil
}

func walkJSON(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, child := range t {
			walkJSON(child, fn)
		}
	case []any:
		for _, child := range t {
			walkJSON(child, fn)
		}
	}
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			case float64:
				return fmt.Sprintf("%.0f", t)
			case json.Number:
				return t.String()
			}
		}
	}
	return ""
}

func floatField(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return t
			case json.Number:
				f, _ := t.Float64()
				return f
			case string:
				var f float64
				fmt.Sscanf(t, "%f", &f)
				return f
			case int:
				return float64(t)
			}
		}
	}
	return 0
}

func teamsMatch(home, away, a, b string) bool {
	h, aw := normTeam(home), normTeam(away)
	x, y := normTeam(a), normTeam(b)
	if h == "" && aw == "" {
		return false
	}
	return (containsTeam(h, x) && containsTeam(aw, y)) || (containsTeam(h, y) && containsTeam(aw, x))
}

func normTeam(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	return s
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

func ifelse(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func (s *Service) getCache(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	// TTL stored implicitly: we set expire by rewriting; use listTTL default check via at+ttl in set
	// store absolute expiry in at field as deadline
	if time.Now().After(e.at) {
		delete(s.cache, key)
		return nil, false
	}
	return e.data, true
}

func (s *Service) setCache(key string, data any, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// prevent unbounded growth
	if len(s.cache) > 200 {
		s.cache = map[string]cacheEntry{}
	}
	s.cache[key] = cacheEntry{at: time.Now().Add(ttl), data: data}
}
