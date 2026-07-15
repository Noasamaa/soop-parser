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

	"github.com/Noasamaa/soop-parser/internal/errs"
	"github.com/Noasamaa/soop-parser/internal/model"
)

var (
	urlRe       = regexp.MustCompile(`(?i)(?:(?:www|m)\.)?huya\.com/([A-Za-z0-9_-]+)`)
	streamB64Re = regexp.MustCompile(`"stream"\s*:\s*"([A-Za-z0-9+/=]+)"`)
	liveLineRe  = regexp.MustCompile(`"liveLineUrl"\s*:\s*"([^"]+)"`)
	roomNameRe  = regexp.MustCompile(`"roomName"\s*:\s*"([^"]*)"`)
	nickRe      = regexp.MustCompile(`"nick"\s*:\s*"([^"]*)"`)
)

const (
	ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

	// streamlink-aligned constants for wsSecret
	constT     = 100
	constVer   = 1
	constSV    = 2401090219
	constCodec = 264

	maxPageBody = 4 << 20
)

// Client resolves Huya live FLV URLs (direct CDN).
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
// Bare room numbers alone are not matched (ambiguous with Bilibili).
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
			LiveID   string `json:"liveId"`
			Nick     string `json:"nick"`
			RoomName string `json:"roomName"`
		} `json:"gameLiveInfo"`
		GameStreamInfoList []streamInfo `json:"gameStreamInfoList"`
	} `json:"data"`
	VMultiStreamInfo []multiBitrate `json:"vMultiStreamInfo"`
}

type streamInfo struct {
	SCdnType      string `json:"sCdnType"`
	SStreamName   string `json:"sStreamName"`
	SFlvURL       string `json:"sFlvUrl"`
	SFlvURLSuffix string `json:"sFlvUrlSuffix"`
	SFlvAntiCode  string `json:"sFlvAntiCode"`
}

type multiBitrate struct {
	IBitRate int `json:"iBitRate"`
}

// Resolve extracts multi-bitrate FLV URLs for a Huya live room.
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
		if u, title, nick, ok := c.tryMobile(ctx, room); ok {
			return &model.Result{
				Channel:  room,
				BNO:      room,
				Title:    title,
				Author:   nick,
				Platform: "huya",
				IsLive:   true,
				Qualities: []model.Quality{{
					Label: "原画 FLV", Name: "source", DirectURL: u, Protocol: "progressive",
				}},
			}, nil
		}
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
	streams := root.Data[0].GameStreamInfoList
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
	sort.SliceStable(streams, func(i, j int) bool {
		return cdnRank(streams[i].SCdnType) < cdnRank(streams[j].SCdnType)
	})

	var qualities []model.Quality
	seen := map[string]bool{}
	for _, s := range streams {
		if s.SFlvURL == "" || s.SStreamName == "" {
			continue
		}
		suffix := s.SFlvURLSuffix
		if suffix == "" {
			suffix = "flv"
		}
		base := ensureHTTPS(strings.TrimRight(s.SFlvURL, "/") + "/" + s.SStreamName + "." + suffix)
		anti := html.UnescapeString(s.SFlvAntiCode)
		for _, br := range bitrates {
			params, err := buildStreamParams(anti, s.SStreamName, br.IBitRate)
			if err != nil {
				continue
			}
			name := fmt.Sprintf("%s_%s", strings.ToLower(s.SCdnType), bitrateName(br.IBitRate))
			if seen[name] {
				continue
			}
			seen[name] = true
			qualities = append(qualities, model.Quality{
				Label:     bitrateLabel(br.IBitRate) + " FLV",
				Name:      name,
				DirectURL: base + "?" + params.Encode(),
				Protocol:  "progressive",
			})
		}
		if len(qualities) > 0 {
			break // one CDN line is enough
		}
	}
	if len(qualities) == 0 {
		return nil, errs.ResolveFailed("未能构造虎牙流地址（antiCode 解析失败或接口变更）")
	}

	bno := info.LiveID
	if bno == "" {
		bno = room
	}
	return &model.Result{
		Channel:   room,
		BNO:       bno,
		Title:     info.RoomName,
		Author:    info.Nick,
		Platform:  "huya",
		IsLive:    true,
		Qualities: qualities,
	}, nil
}

func cdnRank(t string) int {
	switch strings.ToUpper(t) {
	case "AL", "ALICE":
		return 0
	case "TX":
		return 1
	case "HS", "HY":
		return 2
	default:
		return 9
	}
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
	idx := strings.Index(page, "hyPlayerConfig")
	if idx < 0 {
		return nil, errs.NotLive("未找到虎牙播放配置（可能未开播或页面结构变更）")
	}
	start := idx - 200
	if start < 0 {
		start = 0
	}
	end := idx + 300000
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

	// Inline JSON: "stream": { ... }
	key := strings.Index(window, `"stream"`)
	if key < 0 {
		key = strings.Index(window, "stream")
	}
	if key >= 0 {
		brace := strings.Index(window[key:], "{")
		if brace >= 0 {
			if obj, ok := extractJSONObject(window[key+brace:]); ok {
				return []byte(obj), nil
			}
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

func buildStreamParams(antiCode, streamName string, bitRate int) (url.Values, error) {
	q, err := url.ParseQuery(strings.TrimPrefix(antiCode, "?"))
	if err != nil {
		return nil, err
	}
	fm := q.Get("fm")
	fs := q.Get("fs")
	ctype := q.Get("ctype")
	if ctype == "" {
		ctype = "huya_live"
	}
	wsTime := q.Get("wsTime")
	if fm == "" || wsTime == "" {
		return nil, fmt.Errorf("missing fm/wsTime")
	}

	fmDec, err := url.QueryUnescape(fm)
	if err != nil {
		fmDec = fm
	}
	fmBytes, err := base64.StdEncoding.DecodeString(fmDec)
	if err != nil {
		return nil, err
	}
	prefix := strings.SplitN(string(fmBytes), "_", 2)[0]

	uid := rand.Intn(9999) + 12340000
	convertUID := (uid<<8 | uid>>(32-8)) & 0xFFFFFFFF
	timestamp := time.Now().UnixMilli()
	seqid := int64(uid) + timestamp

	wsSecretHash := md5hex(fmt.Sprintf("%d|%s|%d", seqid, ctype, constT))
	wsSecret := md5hex(fmt.Sprintf("%s_%d_%s_%s_%s", prefix, convertUID, streamName, wsSecretHash, wsTime))

	out := url.Values{}
	out.Set("wsSecret", wsSecret)
	out.Set("wsTime", wsTime)
	out.Set("ctype", ctype)
	if fs != "" {
		out.Set("fs", fs)
	}
	out.Set("seqid", strconv.FormatInt(seqid, 10))
	out.Set("u", strconv.FormatUint(uint64(convertUID), 10))
	out.Set("sdk_sid", strconv.FormatInt(timestamp, 10))
	out.Set("ratio", strconv.Itoa(bitRate))
	out.Set("t", strconv.Itoa(constT))
	out.Set("ver", strconv.Itoa(constVer))
	out.Set("sv", strconv.Itoa(constSV))
	out.Set("codec", strconv.Itoa(constCodec))
	return out, nil
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (c *Client) tryMobile(ctx context.Context, room string) (streamURL, title, nick string, ok bool) {
	mobileUA := "Mozilla/5.0 (Linux; Android 12; Mobile) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Mobile Safari/537.36"
	text, err := c.fetchText(ctx, "https://m.huya.com/"+room, mobileUA, "https://m.huya.com/")
	if err != nil {
		return "", "", "", false
	}
	m := liveLineRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", "", false
	}
	line := strings.ReplaceAll(m[1], `\u0026`, "&")
	line = strings.ReplaceAll(line, `\/`, `/`)
	decoded := line
	if b, err := base64.StdEncoding.DecodeString(line); err == nil && len(b) > 0 {
		s := string(b)
		if s[0] == '/' || strings.Contains(s, "flv") || strings.Contains(s, "hls") {
			decoded = s
		}
	}
	if decoded == "" || strings.Contains(decoded, "replay") {
		return "", "", "", false
	}
	if u, err := signMobileLine(decoded); err == nil {
		streamURL = u
	} else {
		streamURL = ensureHTTPS(decoded)
		streamURL = strings.ReplaceAll(streamURL, "hls", "flv")
		streamURL = strings.ReplaceAll(streamURL, ".m3u8", ".flv")
	}
	if tr := roomNameRe.FindStringSubmatch(text); tr != nil {
		title = tr[1]
	}
	if nr := nickRe.FindStringSubmatch(text); nr != nil {
		nick = nr[1]
	}
	return streamURL, title, nick, streamURL != ""
}

func signMobileLine(liveLine string) (string, error) {
	liveLine = strings.TrimPrefix(liveLine, "https:")
	liveLine = strings.TrimPrefix(liveLine, "http:")
	if !strings.HasPrefix(liveLine, "//") {
		liveLine = "//" + strings.TrimPrefix(liveLine, "/")
	}
	parts := strings.SplitN(liveLine, "?", 2)
	if len(parts) != 2 {
		return ensureHTTPS(liveLine), nil
	}
	path, query := parts[0], parts[1]
	path = strings.ReplaceAll(path, "hls", "flv")
	path = strings.ReplaceAll(path, ".m3u8", ".flv")

	q, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	fm := q.Get("fm")
	wsTime := q.Get("wsTime")
	if fm == "" || wsTime == "" {
		return ensureHTTPS(path + "?" + query), nil
	}
	fmDec, _ := url.QueryUnescape(fm)
	fmBytes, err := base64.StdEncoding.DecodeString(fmDec)
	if err != nil {
		return "", err
	}
	p := strings.SplitN(string(fmBytes), "_", 2)[0]
	seg := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		seg = path[i+1:]
	}
	seg = strings.TrimSuffix(strings.TrimSuffix(seg, ".flv"), ".m3u8")
	seqid := strconv.FormatInt(time.Now().UnixNano()/100, 10)
	u := "0"
	wsSecret := md5hex(strings.Join([]string{p, u, seg, seqid, wsTime}, "_"))

	out := url.Values{}
	out.Set("wsSecret", wsSecret)
	out.Set("wsTime", wsTime)
	out.Set("u", u)
	out.Set("seqid", seqid)
	for k, vs := range q {
		if k == "fm" || k == "wsTime" || k == "wsSecret" {
			continue
		}
		for _, v := range vs {
			if v != "" {
				out.Add(k, v)
			}
		}
	}
	return ensureHTTPS(path + "?" + out.Encode()), nil
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
