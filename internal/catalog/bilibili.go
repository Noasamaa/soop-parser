package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Pinned B-station EWC-related rooms (metadata always refreshed).
var biliPins = []struct {
	RoomID string
	Role   string
	Label  string
}{
	{RoomID: "6", Role: "主舞台", Label: "EWC 主舞台"},
	// known high-signal casters (IDs verified via search / get_info)
	{RoomID: "545068", Role: "解说", Label: "德云色/笑笑"},
	{RoomID: "22174141", Role: "解说", Label: "赵俊日"},
	{RoomID: "1919216529", Role: "解说", Label: "waywardzz"},
	{RoomID: "21292831", Role: "解说", Label: "月隐空夜"},
	{RoomID: "14709735", Role: "解说", Label: "炫神"},
	{RoomID: "2978291", Role: "解说", Label: "大兔子老师"},
}

var stripHTML = regexp.MustCompile(`<[^>]+>`)

func (s *Service) buildBiliEWC(ctx context.Context) ([]Item, error) {
	byID := map[string]Item{}

	for _, p := range biliPins {
		byID[p.RoomID] = Item{
			ID:       "bili-" + p.RoomID,
			Platform: "bilibili",
			Group:    "ewc-bili",
			Role:     p.Role,
			Label:    p.Label,
			URL:      "https://live.bilibili.com/" + p.RoomID,
			RoomID:   p.RoomID,
			Pinned:   true,
		}
	}

	// Dynamic discovery via search (realtime room list with covers when available)
	if discovered, err := s.discoverBiliSearch(ctx, "EWC英雄联盟"); err == nil {
		for _, it := range discovered {
			if old, ok := byID[it.RoomID]; ok {
				old.Cover = firstNonEmpty(it.Cover, old.Cover)
				old.Title = firstNonEmpty(it.Title, old.Title)
				old.Author = firstNonEmpty(it.Author, old.Author)
				old.Online = it.Online
				old.IsLive = it.IsLive
				byID[it.RoomID] = old
			} else {
				// only keep clearly EWC-related extras
				blob := strings.ToLower(it.Title + " " + it.Author + " " + it.Label)
				if !strings.Contains(blob, "ewc") && !strings.Contains(it.Title, "世俱") {
					continue
				}
				it.Role = "解说"
				byID[it.RoomID] = it
			}
		}
	}
	// also try 副舞台 keyword
	if discovered, err := s.discoverBiliSearch(ctx, "EWC副舞台"); err == nil {
		for _, it := range discovered {
			if old, ok := byID[it.RoomID]; ok {
				if strings.Contains(it.Title, "副舞台") {
					old.Role = "副舞台"
					old.Label = firstNonEmpty(it.Label, "EWC 副舞台")
				}
				old.Title = firstNonEmpty(it.Title, old.Title)
				old.Cover = firstNonEmpty(it.Cover, old.Cover)
				old.IsLive = it.IsLive
				byID[it.RoomID] = old
			} else if strings.Contains(it.Title, "副舞台") || strings.Contains(it.Title, "EWC") {
				it.Role = "副舞台"
				if it.Role == "副舞台" {
					it.Pinned = true
				}
				byID[it.RoomID] = it
			}
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	enriched := s.enrichBiliRooms(ctx, ids)
	for id, meta := range enriched {
		it := byID[id]
		if meta.Title != "" {
			it.Title = meta.Title
			// upgrade role from live title
			if strings.Contains(meta.Title, "主舞台") {
				it.Role = "主舞台"
			} else if strings.Contains(meta.Title, "副舞台") {
				it.Role = "副舞台"
			}
		}
		if meta.Cover != "" {
			it.Cover = meta.Cover
		}
		if meta.Author != "" {
			it.Author = meta.Author
		}
		it.IsLive = meta.IsLive
		it.Online = meta.Online
		if it.Title == "" {
			it.Title = it.Label
		}
		// resolve short_id -> real room still uses short url fine
		byID[id] = it
	}

	out := make([]Item, 0, len(byID))
	for _, it := range byID {
		out = append(out, it)
	}
	sortItems(out)
	if len(out) > 36 {
		out = out[:36]
	}
	return out, nil
}

func (s *Service) discoverBiliSearch(ctx context.Context, keyword string) ([]Item, error) {
	q := url.Values{
		"search_type": {"live_room"},
		"keyword":     {keyword},
		"page":        {"1"},
	}
	rawURL := "https://api.bilibili.com/x/web-interface/search/type?" + q.Encode()
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    *struct {
			Result []struct {
				RoomID      int64  `json:"roomid"`
				Title       string `json:"title"`
				UName       string `json:"uname"`
				Cover       string `json:"cover"`
				UserCover   string `json:"user_cover"`
				LiveStatus  int    `json:"live_status"`
				Online      int64  `json:"online"`
				WatchedShow struct {
					TextSmall string `json:"text_small"`
				} `json:"watched_show"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := s.getJSONBili(ctx, rawURL, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 || resp.Data == nil {
		return nil, fmt.Errorf("bili search code=%d %s", resp.Code, resp.Message)
	}
	var out []Item
	for _, x := range resp.Data.Result {
		rid := strconv.FormatInt(x.RoomID, 10)
		title := cleanHTML(x.Title)
		cover := ensureHTTPS(firstNonEmpty(x.Cover, x.UserCover))
		out = append(out, Item{
			ID:       "bili-" + rid,
			Platform: "bilibili",
			Group:    "ewc-bili",
			Role:     "解说",
			Label:    firstNonEmpty(x.UName, rid),
			URL:      "https://live.bilibili.com/" + rid,
			RoomID:   rid,
			Title:    title,
			Cover:    cover,
			Author:   x.UName,
			IsLive:   x.LiveStatus == 1,
			Online:   x.Online,
		})
	}
	return out, nil
}

type biliRoomMeta struct {
	Title  string
	Cover  string
	Author string
	IsLive bool
	Online int64
}

func (s *Service) enrichBiliRooms(ctx context.Context, roomIDs []string) map[string]biliRoomMeta {
	out := make(map[string]biliRoomMeta, len(roomIDs))
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
			meta, err := s.fetchBiliRoom(ctx, id)
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

func (s *Service) fetchBiliRoom(ctx context.Context, roomID string) (biliRoomMeta, error) {
	u := "https://api.live.bilibili.com/room/v1/Room/get_info?room_id=" + url.QueryEscape(roomID)
	var resp struct {
		Code int `json:"code"`
		Data *struct {
			Title      string `json:"title"`
			UserCover  string `json:"user_cover"`
			Keyframe   string `json:"keyframe"`
			LiveStatus int    `json:"live_status"`
			Online     int64  `json:"online"`
			UID        int64  `json:"uid"`
		} `json:"data"`
	}
	if err := s.getJSONBili(ctx, u, &resp); err != nil {
		return biliRoomMeta{}, err
	}
	if resp.Code != 0 || resp.Data == nil {
		return biliRoomMeta{}, fmt.Errorf("code %d", resp.Code)
	}
	d := resp.Data
	// prefer keyframe when live (realtime frame), else user_cover
	cover := d.UserCover
	if d.LiveStatus == 1 && d.Keyframe != "" {
		cover = d.Keyframe
	}
	return biliRoomMeta{
		Title:  d.Title,
		Cover:  ensureHTTPS(cover),
		IsLive: d.LiveStatus == 1,
		Online: d.Online,
	}, nil
}

func (s *Service) getJSONBili(ctx context.Context, rawURL string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")
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

func cleanHTML(s string) string {
	s = stripHTML.ReplaceAllString(s, "")
	return html.UnescapeString(strings.TrimSpace(s))
}
