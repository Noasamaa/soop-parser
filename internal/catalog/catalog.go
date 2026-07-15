package catalog

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Item is one room card with live-fetched metadata.
type Item struct {
	ID       string `json:"id"`
	Platform string `json:"platform"` // huya | bilibili | soop | youtube
	Group    string `json:"group"`    // ewc-huya | ewc-bili | soop | youtube
	Role     string `json:"role"`     // 主舞台 / 副舞台 / 解说 …
	Label    string `json:"label"`    // short display name (nick / role)
	URL      string `json:"url"`
	RoomID   string `json:"room_id,omitempty"`
	Title    string `json:"title"` // live title (realtime)
	Cover    string `json:"cover"` // live cover / keyframe (realtime)
	Author   string `json:"author,omitempty"`
	IsLive   bool   `json:"is_live"`
	IsReplay bool   `json:"is_replay,omitempty"`
	Online   int64  `json:"online,omitempty"`
	Pinned   bool   `json:"pinned,omitempty"`
}

// Group is a section of the catalog UI.
type Group struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Items []Item `json:"items"`
}

// Result is the full catalog payload.
type Result struct {
	UpdatedAt time.Time `json:"updated_at"`
	Groups    []Group   `json:"groups"`
}

// Service builds live catalog snapshots (cached briefly).
type Service struct {
	http    *http.Client
	mu      sync.Mutex
	cache   *Result
	cacheAt time.Time
	ttl     time.Duration
}

// New creates a catalog service. base may be nil.
func New(base *http.Client) *Service {
	timeout := 20 * time.Second
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
		ttl:  45 * time.Second,
	}
}

// Get returns a cached or freshly built catalog.
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
		// serve stale cache if any
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
	var (
		huyaItems []Item
		biliItems []Item
		huyaErr   error
		biliErr   error
		wg        sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		huyaItems, huyaErr = s.buildHuyaEWC(ctx)
	}()
	go func() {
		defer wg.Done()
		biliItems, biliErr = s.buildBiliEWC(ctx)
	}()
	wg.Wait()

	// partial OK: one platform failing shouldn't blank the whole page
	if huyaErr != nil && biliErr != nil && len(huyaItems) == 0 && len(biliItems) == 0 {
		if huyaErr != nil {
			return nil, huyaErr
		}
		return nil, biliErr
	}

	groups := []Group{
		{ID: "ewc-huya", Title: "虎牙 · EWC 电竞世俱杯", Items: huyaItems},
		{ID: "ewc-bili", Title: "B站 · EWC 电竞世俱杯", Items: biliItems},
		{ID: "soop", Title: "SOOP / YouTube · 台港澳中文", Items: staticSOOPYT()},
	}
	return &Result{UpdatedAt: time.Now().UTC(), Groups: groups}, nil
}

func staticSOOPYT() []Item {
	return []Item{
		{
			ID: "soop-loltw", Platform: "soop", Group: "soop", Role: "台灣中文",
			Label: "台灣中文", URL: "https://play.sooplive.com/loltw", Title: "SOOP 台灣中文賽事",
			Cover: "", Author: "loltw", IsLive: true, Pinned: true, // status unknown → treat as available
		},
		{
			ID: "soop-lckcarry", Platform: "soop", Group: "soop", Role: "LCK 中文",
			Label: "LCK 中文", URL: "https://play.sooplive.com/lckcarry", Title: "LCK 中文轉播",
			Author: "lckcarry", Pinned: true, IsLive: true,
		},
		{
			ID: "yt-lckcarry", Platform: "youtube", Group: "soop", Role: "YouTube",
			Label: "LCK-Carry", URL: "https://www.youtube.com/@LCKCarry/live", Title: "YouTube LCK 中文",
			Pinned: true, IsLive: true,
		},
		{
			ID: "yt-lcp", Platform: "youtube", Group: "soop", Role: "YouTube",
			Label: "LCP / 太平洋", URL: "https://www.youtube.com/@lolesportstw/live", Title: "LoL Esports TW",
			Pinned: true, IsLive: true,
		},
	}
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		// live first (non-replay)
		aLive := a.IsLive && !a.IsReplay
		bLive := b.IsLive && !b.IsReplay
		if aLive != bLive {
			return aLive
		}
		// pinned official stages first
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		// role priority
		if ra, rb := roleRank(a.Role), roleRank(b.Role); ra != rb {
			return ra < rb
		}
		if a.Online != b.Online {
			return a.Online > b.Online
		}
		return a.Label < b.Label
	})
}

func roleRank(role string) int {
	switch role {
	case "主舞台":
		return 0
	case "副舞台":
		return 1
	case "赛事":
		return 2
	case "解说":
		return 3
	default:
		return 5
	}
}

func ensureHTTPS(u string) string {
	if u == "" {
		return ""
	}
	if len(u) > 2 && u[0] == '/' && u[1] == '/' {
		return "https:" + u
	}
	if len(u) > 7 && u[:7] == "http://" {
		return "https://" + u[7:]
	}
	return u
}
