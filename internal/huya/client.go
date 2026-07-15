package huya

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Noasamaa/soop-parser/internal/errs"
	"github.com/Noasamaa/soop-parser/internal/model"
)

// Anti-code algorithm aligned with actively maintained projects (2025–2026):
//   - ihmily/streamget  (pushed 2026-07)
//   - biliup/biliup     (huya anticode / CDN priority, 2025-12+)
// Older streamlink-style convertUID + page wsTime often yields URLs that
// briefly open then stall in PotPlayer (缓冲 0%).

var (
	urlRe       = regexp.MustCompile(`(?i)(?:(?:www|m)\.)?huya\.com/([A-Za-z0-9_-]+)`)
	streamB64Re = regexp.MustCompile(`"stream"\s*:\s*"([A-Za-z0-9+/=]+)"`)
	streamKeyRe = regexp.MustCompile(`(?i)stream\s*:\s*`)
)

const (
	ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	// streamget mobile/web rebuild constants
	paramsT     = 100
	sdkVersion  = 2403051612
	constCodec  = 264
	maxPageBody = 4 << 20
)

// Client resolves Huya live FLV/HLS URLs (direct CDN).
type Client struct {
	http *http.Client
}

// NewClient builds a client. base may be nil.
func NewClient(base *http.Client) *Client {
	timeout := 30 * time.Second
	var transport http.RoundTripper = http.DefaultTransport
	if base != nil {
		if base.Transport != nil {
			transport = base.Transport
		}
		if base.Timeout > 0 {
			timeout = base.Timeout
		}
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: timeout}}
}

// IsURL reports whether raw is a Huya room URL (requires huya.com host).
func IsURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(strings.ToLower(raw), "huya.com") {
		return false
	}
	_, err := ParseRoom(raw)
	return err == nil
}

// ParseRoom returns the room path segment (id or vanity slug).
func ParseRoom(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errs.InvalidURL("请输入虎牙直播间链接")
	}
	if !strings.Contains(raw, "://") && strings.Contains(strings.ToLower(raw), "huya.com") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errs.InvalidURL("无效的虎牙链接")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !strings.Contains(host, "huya.com") {
		return "", errs.InvalidURL("无效的虎牙链接，示例：https://www.huya.com/lck")
	}
	m := urlRe.FindStringSubmatch(raw)
	if m == nil {
		return "", errs.InvalidURL("无效的虎牙直播链接，示例：https://www.huya.com/lck")
	}
	seg := m[1]
	switch strings.ToLower(seg) {
	case "g", "video", "download", "help", "l":
		return "", errs.InvalidURL("无效的虎牙直播间路径")
	}
	return seg, nil
}

type streamRoot struct {
	Data []struct {
		GameLiveInfo struct {
			LiveID       string `json:"liveId"`
			Nick         string `json:"nick"`
			RoomName     string `json:"roomName"`
			Introduction string `json:"introduction"`
			BitRate      int    `json:"bitRate"`
		} `json:"gameLiveInfo"`
		GameStreamInfoList []streamInfo `json:"gameStreamInfoList"`
	} `json:"data"`
	VMultiStreamInfo []multiBitrate `json:"vMultiStreamInfo"`
}

type streamInfo struct {
	SCdnType         string `json:"sCdnType"`
	SStreamName      string `json:"sStreamName"`
	SFlvURL          string `json:"sFlvUrl"`
	SFlvURLSuffix    string `json:"sFlvUrlSuffix"`
	SFlvAntiCode     string `json:"sFlvAntiCode"`
	SHlsURL          string `json:"sHlsUrl"`
	SHlsURLSuffix    string `json:"sHlsUrlSuffix"`
	SHlsAntiCode     string `json:"sHlsAntiCode"`
	IWebPriorityRate int    `json:"iWebPriorityRate"`
	LPresenterUid    any    `json:"lPresenterUid"`
}

type multiBitrate struct {
	IBitRate int    `json:"iBitRate"`
	SDisplay string `json:"sDisplayName"`
}

// Resolve extracts multi-bitrate FLV/HLS URLs for a Huya live room.
func (c *Client) Resolve(ctx context.Context, raw string) (*model.Result, error) {
	room, err := ParseRoom(raw)
	if err != nil {
		return nil, err
	}

	page, err := c.fetchText(ctx, "https://www.huya.com/"+room, ua, "https://www.huya.com/")
	if err != nil {
		return nil, err
	}

	streamJSON, err := extractStreamJSON(page)
	if err != nil {
		return nil, err
	}

	var root streamRoot
	if err := json.Unmarshal(streamJSON, &root); err != nil {
		return nil, errs.ResolveFailed("虎牙 stream JSON 解析失败")
	}
	if len(root.Data) == 0 || len(root.Data[0].GameStreamInfoList) == 0 {
		return nil, errs.NotLive("该虎牙直播间未开播或无可用流")
	}

	info := root.Data[0].GameLiveInfo
	title := firstNonEmpty(info.Introduction, info.RoomName)
	// biliup: 回放/重播不当真直播（PotPlayer 常卡在 0~2s / 缓冲 0%）
	if isReplayTitle(title) {
		return nil, errs.NotLive("该房间当前是回放/重播（非直播），虎牙重播流在播放器里经常无法连续缓冲。请换正在直播的房间。标题：" + truncate(title, 40))
	}

	streams := filterStreams(root.Data[0].GameStreamInfoList)
	if len(streams) == 0 {
		return nil, errs.NotLive("该虎牙直播间无可用 CDN")
	}
	// Prefer TX then AL (streamget / biliup practice); skip HY internal lines
	sort.SliceStable(streams, func(i, j int) bool {
		return cdnRank(streams[i].SCdnType) < cdnRank(streams[j].SCdnType)
	})

	bitrates := root.VMultiStreamInfo
	if len(bitrates) == 0 {
		bitrates = []multiBitrate{{IBitRate: 0}}
	}
	sort.SliceStable(bitrates, func(i, j int) bool {
		ai, aj := bitrates[i].IBitRate, bitrates[j].IBitRate
		if ai == 0 {
			return true
		}
		if aj == 0 {
			return false
		}
		return ai > aj
	})

	// Use best CDN line; rebuild anti-code once per stream name
	s := streams[0]
	streamName := s.SStreamName
	if streamName == "" {
		return nil, errs.ResolveFailed("虎牙 streamName 为空")
	}

	var qualities []model.Quality
	seen := map[string]bool{}

	// FLV multi-ratio
	if s.SFlvURL != "" {
		anti := html.UnescapeString(s.SFlvAntiCode)
		query, err := rebuildAntiCode(anti, streamName)
		if err != nil {
			return nil, errs.ResolveFailed("虎牙防盗链参数生成失败: " + err.Error())
		}
		suffix := s.SFlvURLSuffix
		if suffix == "" {
			suffix = "flv"
		}
		base := ensureHTTPS(strings.TrimRight(s.SFlvURL, "/") + "/" + streamName + "." + suffix)
		for _, br := range bitrates {
			q := cloneValues(query)
			if br.IBitRate > 0 {
				q.Set("ratio", strconv.Itoa(br.IBitRate))
			} else {
				q.Del("ratio") // 原画不带 ratio（streamget 用 ratio= 空）
			}
			name := fmt.Sprintf("%s_flv_%s", strings.ToLower(s.SCdnType), bitrateName(br.IBitRate))
			if seen[name] {
				continue
			}
			seen[name] = true
			qualities = append(qualities, model.Quality{
				Label:     bitrateLabel(br.IBitRate) + " FLV",
				Name:      name,
				DirectURL: base + "?" + q.Encode(),
				Protocol:  "progressive",
			})
		}
	}

	// HLS (often more stable in multi-request players than single FLV)
	if s.SHlsURL != "" {
		anti := html.UnescapeString(firstNonEmpty(s.SHlsAntiCode, s.SFlvAntiCode))
		query, err := rebuildAntiCode(anti, streamName)
		if err == nil {
			suffix := s.SHlsURLSuffix
			if suffix == "" {
				suffix = "m3u8"
			}
			base := ensureHTTPS(strings.TrimRight(s.SHlsURL, "/") + "/" + streamName + "." + suffix)
			// one source HLS first for browser / PotPlayer
			name := fmt.Sprintf("%s_hls_source", strings.ToLower(s.SCdnType))
			if !seen[name] {
				seen[name] = true
				// Put HLS near top after first FLV source for browser friendliness:
				// insert after first quality if present
				hq := model.Quality{
					Label:     "原画 HLS",
					Name:      name,
					DirectURL: base + "?" + query.Encode(),
					Protocol:  "hls",
				}
				if len(qualities) > 0 {
					// keep FLV source first (PotPlayer FLV path), then HLS
					qualities = append(qualities[:1], append([]model.Quality{hq}, qualities[1:]...)...)
				} else {
					qualities = append(qualities, hq)
				}
			}
		}
	}

	if len(qualities) == 0 {
		return nil, errs.ResolveFailed("未能构造虎牙流地址")
	}

	bno := firstNonEmpty(info.LiveID, room)
	return &model.Result{
		Channel:   room,
		BNO:       bno,
		Title:     title,
		Author:    info.Nick,
		Platform:  "huya",
		IsLive:    true,
		Qualities: qualities,
	}, nil
}

func filterStreams(in []streamInfo) []streamInfo {
	var out []streamInfo
	for _, s := range in {
		cdn := strings.ToUpper(s.SCdnType)
		// biliup skips HY / HUYA / HYZJ internal lines
		if cdn == "HY" || cdn == "HUYA" || cdn == "HYZJ" {
			continue
		}
		if s.IWebPriorityRate < 0 {
			continue
		}
		if s.SStreamName == "" || (s.SFlvURL == "" && s.SHlsURL == "") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func cdnRank(t string) int {
	switch strings.ToUpper(t) {
	case "TX":
		return 0 // streamget prefers TX
	case "AL", "ALICE":
		return 1
	case "HW":
		return 2
	case "HS":
		return 3
	default:
		return 9
	}
}

// rebuildAntiCode ports ihmily/streamget get_anti_code (mobile/web rebuild).
// Key differences vs old streamlink: large uid, regenerated hex wsTime,
// secret uses raw uid (not convertUID), includes uuid.
func rebuildAntiCode(oldAnti, streamName string) (url.Values, error) {
	q, err := url.ParseQuery(strings.TrimPrefix(oldAnti, "?"))
	if err != nil {
		return nil, err
	}
	fm := q.Get("fm")
	if fm == "" {
		return nil, fmt.Errorf("missing fm")
	}
	ctype := q.Get("ctype")
	if ctype == "" {
		ctype = "huya_live"
	}
	fs := q.Get("fs")
	if fs == "" {
		fs = "bgct"
	}

	fmDec, err := url.QueryUnescape(fm)
	if err != nil {
		fmDec = fm
	}
	fmBytes, err := base64.StdEncoding.DecodeString(fmDec)
	if err != nil {
		return nil, fmt.Errorf("fm base64: %w", err)
	}
	prefix := strings.SplitN(string(fmBytes), "_", 2)[0]

	t13 := time.Now().UnixMilli()
	sdkSid := t13
	// streamget: uid in 1400000000000..1400009999999 range
	uid := int64(1400000000000 + rand.Intn(1_000_000))
	seqID := uid + sdkSid
	// uuid: (t13 % 1e10 * 1000 + random) % 2^32
	initUUID := (int64(float64(t13%10_000_000_000)*1000) + int64(rand.Intn(1000))) % 4294967295
	// wsTime: (now_ms + 110624) // 1000 as hex
	wsTime := fmt.Sprintf("%x", (t13+110624)/1000)

	wsSecretHash := md5hex(fmt.Sprintf("%d|%s|%d", seqID, ctype, paramsT))
	wsSecret := md5hex(fmt.Sprintf("%s_%d_%s_%s_%s", prefix, uid, streamName, wsSecretHash, wsTime))

	out := url.Values{}
	out.Set("wsSecret", wsSecret)
	out.Set("wsTime", wsTime)
	out.Set("seqid", strconv.FormatInt(seqID, 10))
	out.Set("ctype", ctype)
	out.Set("ver", "1")
	out.Set("fs", fs)
	out.Set("uuid", strconv.FormatInt(initUUID, 10))
	out.Set("u", strconv.FormatInt(uid, 10))
	out.Set("t", strconv.Itoa(paramsT))
	out.Set("sv", strconv.Itoa(sdkVersion))
	out.Set("sdk_sid", strconv.FormatInt(sdkSid, 10))
	out.Set("codec", strconv.Itoa(constCodec))
	return out, nil
}

func (c *Client) fetchText(ctx context.Context, pageURL, userAgent, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", errs.WrapResolve(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBody))
	if err != nil {
		return "", errs.WrapResolve(err)
	}
	if resp.StatusCode >= 400 {
		return "", errs.ResolveFailed(fmt.Sprintf("虎牙页面 HTTP %d", resp.StatusCode))
	}
	return string(body), nil
}

func extractStreamJSON(page string) ([]byte, error) {
	// Prefer hyPlayerConfig window then stream: {json}
	idx := strings.Index(page, "hyPlayerConfig")
	if idx < 0 {
		idx = strings.Index(page, "stream:")
	}
	if idx < 0 {
		return nil, errs.NotLive("未找到虎牙播放配置（可能未开播或页面结构变更）")
	}
	start := idx - 100
	if start < 0 {
		start = 0
	}
	end := idx + 400000
	if end > len(page) {
		end = len(page)
	}
	window := page[start:end]

	if m := streamB64Re.FindStringSubmatch(window); m != nil {
		dec, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			return nil, errs.ResolveFailed("虎牙 stream base64 解码失败")
		}
		return dec, nil
	}

	// stream: {...}  — balanced object (streamget: cut before iWebDefaultBitRate)
	if loc := streamKeyRe.FindStringIndex(window); loc != nil {
		rest := window[loc[1]:]
		rest = strings.TrimLeft(rest, " \t\n\r")
		if obj, ok := extractJSONObject(rest); ok {
			return []byte(obj), nil
		}
	}
	return nil, errs.NotLive("该虎牙直播间未开播或无 stream 数据")
}

func extractJSONObject(s string) (string, bool) {
	if s == "" || s[0] != '{' {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1], true
			}
		}
	}
	return "", false
}

// isReplayTitle mirrors biliup: keyword in first/last 3 runes.
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
	return false
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ensureHTTPS(u string) string {
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "http://") {
		return "https://" + strings.TrimPrefix(u, "http://")
	}
	if strings.HasPrefix(u, "https://") {
		return u
	}
	return "https://" + strings.TrimPrefix(u, "/")
}

func bitrateLabel(br int) string {
	if br == 0 {
		return "原画"
	}
	if br >= 1000 {
		return fmt.Sprintf("%dk", br)
	}
	return strconv.Itoa(br)
}

func bitrateName(br int) string {
	if br == 0 {
		return "source"
	}
	return strconv.Itoa(br) + "k"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
