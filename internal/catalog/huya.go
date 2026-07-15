package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Official / high-value pins (always shown; metadata refreshed live).
var huyaPins = []struct {
	RoomID string
	Host   string
	Role   string
	Label  string
}{
	{RoomID: "660116", Host: "saewc", Role: "主舞台", Label: "EWC 主舞台"},
	{RoomID: "660137", Host: "1758865528", Role: "副舞台", Label: "英雄联盟副舞台"},
	{RoomID: "660000", Host: "lpl", Role: "赛事", Label: "虎牙英雄联盟赛事"},
	// common casters on saewc tab bar (IDs from live discovery)
	{RoomID: "333003", Host: "2369247112", Role: "解说", Label: "姿态"},
	{RoomID: "579236", Host: "06016sask", Role: "解说", Label: "sask"},
	{RoomID: "890001", Host: "968316902", Role: "解说", Label: "957"},
	{RoomID: "149361", Host: "1925949926", Role: "解说", Label: "米勒"},
	{RoomID: "528222", Host: "rememberlol", Role: "解说", Label: "记得"},
	{RoomID: "323444", Host: "", Role: "解说", Label: "硕硕"},
	{RoomID: "149346", Host: "1267235383", Role: "解说", Label: "毛毛"},
	{RoomID: "262985", Host: "jiaozisang", Role: "解说", Label: "海威"},
	{RoomID: "157618", Host: "", Role: "解说", Label: "wink"},
	{RoomID: "156397", Host: "", Role: "解说", Label: "解说凡凡"},
	{RoomID: "691406", Host: "2205595990", Role: "解说", Label: "baicaiovo"},
	{RoomID: "699772", Host: "xinghen", Role: "解说", Label: "星痕OB"},
	{RoomID: "886673", Host: "jvhua", Role: "解说", Label: "菊花"},
	{RoomID: "222523", Host: "1778653381", Role: "解说", Label: "kRYST4L"},
}

var huyaKeywords = []string{
	"ewc", "世俱", "saewc", "主舞台", "副舞台",
}

func (s *Service) buildHuyaEWC(ctx context.Context) ([]Item, error) {
	// roomID -> base item from discovery or pins
	byID := map[string]Item{}

	// 1) pins first
	for _, p := range huyaPins {
		host := p.Host
		if host == "" {
			host = p.RoomID
		}
		byID[p.RoomID] = Item{
			ID:       "huya-" + p.RoomID,
			Platform: "huya",
			Group:    "ewc-huya",
			Role:     p.Role,
			Label:    p.Label,
			URL:      "https://www.huya.com/" + host,
			RoomID:   p.RoomID,
			Pinned:   true,
		}
	}

	// 2) dynamic discovery from LoL live list (realtime IDs + covers)
	discovered, err := s.discoverHuyaLive(ctx)
	if err == nil {
		for _, it := range discovered {
			if old, ok := byID[it.RoomID]; ok {
				// keep pin role/label, refresh cover/title/online from list
				old.Cover = it.Cover
				old.Title = it.Title
				old.Author = it.Author
				old.Online = it.Online
				old.IsLive = it.IsLive
				if it.URL != "" {
					old.URL = it.URL
				}
				byID[it.RoomID] = old
			} else {
				it.Role = "解说"
				it.Pinned = false
				byID[it.RoomID] = it
			}
		}
	}

	// 3) enrich missing/outdated via profileRoom (title/cover/live)
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	enriched := s.enrichHuyaProfiles(ctx, ids)
	for id, meta := range enriched {
		it := byID[id]
		if meta.Title != "" {
			it.Title = meta.Title
		}
		if meta.Cover != "" {
			it.Cover = meta.Cover
		}
		if meta.Author != "" {
			it.Author = meta.Author
			if !it.Pinned || it.Label == "" {
				it.Label = meta.Author
			}
		}
		if meta.Host != "" {
			it.URL = "https://www.huya.com/" + meta.Host
		}
		it.IsLive = meta.IsLive
		it.IsReplay = meta.IsReplay
		if it.IsReplay {
			it.IsLive = false
		}
		if it.Title == "" {
			it.Title = it.Label
		}
		byID[id] = it
	}

	out := make([]Item, 0, len(byID))
	for _, it := range byID {
		out = append(out, it)
	}
	sortItems(out)
	// cap list to keep UI usable
	if len(out) > 36 {
		out = out[:36]
	}
	return out, nil
}

type huyaListResp struct {
	Status int `json:"status"`
	Data   struct {
		Datas []huyaListItem `json:"datas"`
	} `json:"data"`
}

type huyaListItem struct {
	ProfileRoom  string `json:"profileRoom"`
	PrivateHost  string `json:"privateHost"`
	Nick         string `json:"nick"`
	Introduction string `json:"introduction"`
	RoomName     string `json:"roomName"`
	Screenshot   string `json:"screenshot"`
	Avatar180    string `json:"avatar180"`
	TotalCount   string `json:"totalCount"`
}

func (s *Service) discoverHuyaLive(ctx context.Context) ([]Item, error) {
	var out []Item
	seen := map[string]bool{}
	for page := 1; page <= 4; page++ {
		if ctx.Err() != nil {
			break
		}
		u := fmt.Sprintf("https://www.huya.com/cache.php?m=LiveList&do=getLiveListByPage&gameId=1&tagAll=0&page=%d", page)
		var resp huyaListResp
		if err := s.getJSON(ctx, u, &resp); err != nil {
			if page == 1 {
				return nil, err
			}
			break
		}
		for _, x := range resp.Data.Datas {
			blob := strings.ToLower(x.Nick + " " + x.Introduction + " " + x.RoomName + " " + x.PrivateHost)
			if !matchAny(blob, huyaKeywords) {
				continue
			}
			rid := x.ProfileRoom
			if rid == "" || seen[rid] {
				continue
			}
			seen[rid] = true
			host := x.PrivateHost
			if host == "" {
				host = rid
			}
			online, _ := strconv.ParseInt(x.TotalCount, 10, 64)
			title := firstNonEmpty(x.Introduction, x.RoomName, x.Nick)
			cover := ensureHTTPS(firstNonEmpty(x.Screenshot, x.Avatar180))
			out = append(out, Item{
				ID:       "huya-" + rid,
				Platform: "huya",
				Group:    "ewc-huya",
				Role:     "解说",
				Label:    firstNonEmpty(x.Nick, rid),
				URL:      "https://www.huya.com/" + host,
				RoomID:   rid,
				Title:    title,
				Cover:    cover,
				Author:   x.Nick,
				IsLive:   true,
				Online:   online,
			})
		}
	}
	return out, nil
}

type huyaProfileMeta struct {
	Title    string
	Cover    string
	Author   string
	Host     string
	IsLive   bool
	IsReplay bool
}

func (s *Service) enrichHuyaProfiles(ctx context.Context, roomIDs []string) map[string]huyaProfileMeta {
	out := make(map[string]huyaProfileMeta, len(roomIDs))
	var mu sync.Mutex
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, id := range roomIDs {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			meta, err := s.fetchHuyaProfile(ctx, id)
			if err != nil {
				return
			}
			mu.Lock()
			out[id] = meta
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func (s *Service) fetchHuyaProfile(ctx context.Context, roomID string) (huyaProfileMeta, error) {
	u := "https://mp.huya.com/cache.php?m=Live&do=profileRoom&roomid=" + url.QueryEscape(roomID) + "&showSecret=1"
	var raw struct {
		Status int `json:"status"`
		Data   *struct {
			LiveStatus     string `json:"liveStatus"`
			RealLiveStatus string `json:"realLiveStatus"`
			ProfileInfo    *struct {
				Nick        string `json:"nick"`
				PrivateHost string `json:"privateHost"`
				ProfileRoom any    `json:"profileRoom"`
				AvatarURL   string `json:"avatarUrl180"`
			} `json:"profileInfo"`
			LiveData *struct {
				Introduction string `json:"introduction"`
				RoomName     string `json:"roomName"`
				Screenshot   string `json:"screenshot"`
			} `json:"liveData"`
		} `json:"data"`
	}
	if err := s.getJSON(ctx, u, &raw); err != nil {
		return huyaProfileMeta{}, err
	}
	if raw.Data == nil {
		return huyaProfileMeta{}, fmt.Errorf("empty profile")
	}
	d := raw.Data
	live := strings.EqualFold(d.LiveStatus, "ON") || strings.EqualFold(d.RealLiveStatus, "ON")
	var title, cover, author, host string
	if d.ProfileInfo != nil {
		author = d.ProfileInfo.Nick
		host = d.ProfileInfo.PrivateHost
		if cover == "" {
			cover = d.ProfileInfo.AvatarURL
		}
	}
	if d.LiveData != nil {
		title = firstNonEmpty(d.LiveData.Introduction, d.LiveData.RoomName)
		if d.LiveData.Screenshot != "" {
			cover = d.LiveData.Screenshot
		}
	}
	replay := isReplayTitle(title)
	return huyaProfileMeta{
		Title:    title,
		Cover:    ensureHTTPS(cover),
		Author:   author,
		Host:     host,
		IsLive:   live && !replay,
		IsReplay: replay,
	}, nil
}

func (s *Service) getJSON(ctx context.Context, rawURL string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://www.huya.com/")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.Unmarshal(body, dest)
}

func matchAny(blob string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(blob, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func isReplayTitle(title string) bool {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) == 0 {
		return false
	}
	head := string(runes[:min(3, len(runes))])
	tailN := min(3, len(runes))
	tail := string(runes[len(runes)-tailN:])
	for _, kw := range []string{"回放", "重播"} {
		if strings.Contains(head, kw) || strings.Contains(tail, kw) {
			return true
		}
	}
	// also mid-title markers common on huya
	if strings.Contains(title, "【重播】") || strings.Contains(title, "【回放】") {
		return true
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
