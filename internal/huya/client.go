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
	urlRe = regexp.MustCompile(`(?i)(?:(?:www|m)\.)?huya\.com/([A-Za-z0-9_-]+)`)
	// hyPlayerConfig stream: base64 string or inline JSON
	streamRe = regexp.MustCompile(`(?s)"?stream"?\s*:\s*(?:"([^"]+)"|(\{.+?\})\s*\}\s*;)`)
)

const (
	ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

	// streamlink-aligned constants for wsSecret
	constT     = 100
	constVer   = 1
	constSV    = 2401090219
	constCodec = 264
)

// Client resolves Huya live FLV URLs (direct CDN; no proxy needed in CN).
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
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
	}
}

// IsURL reports whether raw is a Huya room URL or room id/slug.
func IsURL(raw string) bool {
	_, err := ParseRoom(raw)
	return err == nil
}

// ParseRoom returns the room path segment (id or vanity slug).
func ParseRoom(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errs.InvalidURL("请输入虎牙直播间链接或房间号")
	}
	// bare slug / numeric id
	if !strings.Contains(raw, "/") && !strings.Contains(raw, ".") {
		if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(raw) {
			return raw, nil
		}
	}
	if !strings.Contains(raw, "://") && strings.Contains(strings.ToLower(raw), "huya.com") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errs.InvalidURL("无效的虎牙链接")
	}
	host := strings.ToLower(u.Host)
	if host != "" && !strings.Contains(host, "huya.com") {
		return "", errs.InvalidURL("无效的虎牙链接")
	}
	if m := urlRe.FindStringSubmatch(raw); m != nil {
		// skip reserved paths
		seg := m[1]
		switch strings.ToLower(seg) {
		case "g", "video", "download", "help", "l":
			return "", errs.InvalidURL("无效的虎牙直播间路径")
		}
		return seg, nil
	}
	return "", errs.InvalidURL("无效的虎牙直播链接，示例：https://www.huya.com/lck")
}

// Resolve extracts multi-bitrate FLV URLs for a Huya live room.
func (c *Client) Resolve(ctx context.Context, raw string) (*model.Result, error) {
	room, err := ParseRoom(raw)
	if err != nil {
		return nil, err
	}

	pageURL := "https://www.huya.com/" + room
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.huya.com/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errs.WrapResolve(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, errs.WrapResolve(err)
	}
	if resp.StatusCode >= 400 {
		return nil, errs.ResolveFailed(fmt.Sprintf("虎牙页面 HTTP %d", resp.StatusCode))
	}

	streamJSON, err := extractStreamJSON(string(body))
	if err != nil {
		// fallback: mobile page liveLineUrl (single quality)
		if u, title, nick, ok := c.tryMobile(ctx, room); ok {
			return &model.Result{
				Channel:   room,
				BNO:       room,
				Title:     title,
				Author:    nick,
				Platform:  "huya",
				IsLive:    true,
				Qualities: []model.Quality{{Label: "原画 FLV", Name: "source", DirectURL: u, Protocol: "progressive"}},
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
	// sort high first (0 = source = highest)
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

	// prefer common CDNs order: AL, TX, HS, others
	cdnRank := func(t string) int {
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
	sort.SliceStable(streams, func(i, j int) bool {
		return cdnRank(streams[i].SCdnType) < cdnRank(streams[j].SCdnType)
	})

	var qualities []model.Quality
	seen := map[string]bool{}

	// Use first good CDN line; multi bitrate via ratio param
	for _, s := range streams {
		if s.SFlvURL == "" || s.SStreamName == "" {
			continue
		}
		base := ensureHTTPS(strings.TrimRight(s.SFlvURL, "/") + "/" + s.SStreamName + "." + orDefault(s.SFlvURLSuffix, "flv"))
		anti := html.UnescapeString(s.SFlvAntiCode)
		for _, br := range bitrates {
			params, err := buildStreamParams(anti, s.SStreamName, br.IBitRate)
			if err != nil {
				continue
			}
			full := base + "?" + params.Encode()
			label := bitrateLabel(br.IBitRate)
			name := fmt.Sprintf("%s_%s", strings.ToLower(s.SCdnType), bitrateName(br.IBitRate))
			if seen[name] {
				continue
			}
			seen[name] = true
			qualities = append(qualities, model.Quality{
				Label:     label + " FLV",
				Name:      name,
				DirectURL: full,
				Protocol:  "progressive",
			})
		}
		// one CDN is enough for playback; avoid flooding list
		if len(qualities) > 0 {
			break
		}
	}

	if len(qualities) == 0 {
		return nil, errs.ResolveFailed("未能构造虎牙流地址（antiCode 解析失败或接口变更）")
	}

	return &model.Result{
		Channel:   room,
		BNO:       firstNonEmpty(info.LiveID, room),
		Title:     info.RoomName,
		Author:    info.Nick,
		Platform:  "huya",
		IsLive:    true,
		Qualities: qualities,
	}, nil
}

type streamRoot struct {
	Data []struct {
		GameLiveInfo struct {
			LiveID   string `json:"liveId"`
			Nick     string `json:"nick"`
			RoomName string `json:"roomName"`
		} `json:"gameLiveInfo"`
		GameStreamInfoList []struct {
			SCdnType      string `json:"sCdnType"`
			SStreamName   string `json:"sStreamName"`
			SFlvURL       string `json:"sFlvUrl"`
			SFlvURLSuffix string `json:"sFlvUrlSuffix"`
			SFlvAntiCode  string `json:"sFlvAntiCode"`
			SHlsURL       string `json:"sHlsUrl"`
			SHlsURLSuffix string `json:"sHlsUrlSuffix"`
			SHlsAntiCode  string `json:"sHlsAntiCode"`
		} `json:"gameStreamInfoList"`
	} `json:"data"`
	VMultiStreamInfo []multiBitrate `json:"vMultiStreamInfo"`
}

type multiBitrate struct {
	IBitRate int    `json:"iBitRate"`
	SDisplay string `json:"sDisplayName"`
}

func extractStreamJSON(page string) ([]byte, error) {
	// Prefer script containing hyPlayerConfig
	idx := strings.Index(page, "hyPlayerConfig")
	if idx < 0 {
		return nil, errs.NotLive("未找到虎牙播放配置（可能未开播或页面结构变更）")
	}
	// search around config
	window := page
	if idx > 0 {
		start := idx - 200
		if start < 0 {
			start = 0
		}
		end := idx + 500000
		if end > len(page) {
			end = len(page)
		}
		window = page[start:end]
	}
	m := streamRe.FindStringSubmatch(window)
	if m == nil {
		// broader: "stream":{...} nested in config — try base64 only
		b64re := regexp.MustCompile(`"stream"\s*:\s*"([A-Za-z0-9+/=]+)"`)
		if bm := b64re.FindStringSubmatch(window); bm != nil {
			dec, err := base64.StdEncoding.DecodeString(bm[1])
			if err != nil {
				return nil, errs.ResolveFailed("虎牙 stream base64 解码失败")
			}
			return dec, nil
		}
		return nil, errs.NotLive("该虎牙直播间未开播或无 stream 数据")
	}
	if m[1] != "" {
		dec, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			// try raw URL-safe
			dec, err = base64.RawStdEncoding.DecodeString(m[1])
			if err != nil {
				return nil, errs.ResolveFailed("虎牙 stream base64 解码失败")
			}
		}
		return dec, nil
	}
	// inline JSON object — m[2] may be truncated by non-greedy; re-extract balanced
	raw := m[2]
	if raw == "" {
		return nil, errs.ResolveFailed("虎牙 stream 为空")
	}
	// if incomplete, try to find full object from "stream":
	if !json.Valid([]byte(raw)) {
		key := strings.Index(window, `"stream"`)
		if key < 0 {
			key = strings.Index(window, "stream")
		}
		if key >= 0 {
			brace := strings.Index(window[key:], "{")
			if brace >= 0 {
				obj, ok := extractJSONObject(window[key+brace:])
				if ok {
					return []byte(obj), nil
				}
			}
		}
		return nil, errs.ResolveFailed("虎牙 stream JSON 不完整")
	}
	return []byte(raw), nil
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
	q, err := url.ParseQuery(antiCode)
	if err != nil {
		// antiCode is already query-like without leading ?
		q, err = url.ParseQuery(strings.TrimPrefix(antiCode, "?"))
		if err != nil {
			return nil, err
		}
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

	// fm is url-encoded base64
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
	pageURL := "https://m.huya.com/" + room
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", "", "", false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 12; Mobile) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Mobile Safari/537.36")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", "", false
	}
	text := string(body)
	// "liveLineUrl":"...." may be plain or base64
	re := regexp.MustCompile(`"liveLineUrl"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return "", "", "", false
	}
	line := m[1]
	// unescape unicode/json
	line = strings.ReplaceAll(line, `\u0026`, "&")
	line = strings.ReplaceAll(line, `\/`, `/`)
	decoded := line
	if b, err := base64.StdEncoding.DecodeString(line); err == nil && len(b) > 0 && (b[0] == '/' || strings.Contains(string(b), "flv")) {
		decoded = string(b)
	}
	if decoded == "" || strings.Contains(decoded, "replay") {
		return "", "", "", false
	}
	if u, err := signMobileLine(decoded); err == nil {
		streamURL = u
	} else {
		streamURL = ensureHTTPS(decoded)
		// force flv
		streamURL = strings.ReplaceAll(streamURL, "hls", "flv")
		streamURL = strings.ReplaceAll(streamURL, ".m3u8", ".flv")
	}
	if tr := regexp.MustCompile(`"roomName"\s*:\s*"([^"]*)"`).FindStringSubmatch(text); tr != nil {
		title = tr[1]
	}
	if nr := regexp.MustCompile(`"nick"\s*:\s*"([^"]*)"`).FindStringSubmatch(text); nr != nil {
		nick = nr[1]
	}
	return streamURL, title, nick, streamURL != ""
}

// signMobileLine applies the classic liveLineUrl wsSecret algorithm.
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
	// replace hls->flv
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
	// stream name from path
	seg := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		seg = path[i+1:]
	}
	seg = strings.TrimSuffix(seg, ".flv")
	seg = strings.TrimSuffix(seg, ".m3u8")
	seqid := strconv.FormatInt(time.Now().UnixNano()/100, 10)
	u := "0"
	h := strings.Join([]string{p, u, seg, seqid, wsTime}, "_")
	wsSecret := md5hex(h)

	// rebuild query: keep non-empty original trailing params loosely
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

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
