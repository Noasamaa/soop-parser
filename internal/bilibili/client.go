package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	urlRe = regexp.MustCompile(`(?i)live\.bilibili\.com/(?:h5/|blanc/)?(\d+)`)
	idRe  = regexp.MustCompile(`^\d{1,12}$`)
)

var qnLabels = map[int]string{
	30000: "杜比",
	20000: "4K",
	10000: "原画",
	400:   "蓝光",
	250:   "超清",
	150:   "高清",
	80:    "流畅",
}

const (
	ua          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	roomInitAPI = "https://api.live.bilibili.com/room/v1/Room/room_init"
	playInfoAPI = "https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo"
	roomInfoAPI = "https://api.live.bilibili.com/room/v1/Room/get_info"
	maxQnFetch  = 5 // cap sequential playInfo calls
	maxBody     = 2 << 20
)

// Client resolves Bilibili live play URLs (direct CDN).
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

// IsURL reports whether raw is a Bilibili live link or bare room id.
func IsURL(raw string) bool {
	_, err := ParseRoomID(raw)
	return err == nil
}

// ParseRoomID extracts a room id from URL or bare digits.
func ParseRoomID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errs.InvalidURL("请输入 Bilibili 直播间链接或房间号")
	}
	if idRe.MatchString(raw) {
		return raw, nil
	}
	if !strings.Contains(raw, "://") && strings.Contains(strings.ToLower(raw), "bilibili") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errs.InvalidURL("无效的 Bilibili 链接")
	}
	host := strings.ToLower(u.Hostname())
	if host != "" && !strings.Contains(host, "bilibili.com") {
		return "", errs.InvalidURL("无效的 Bilibili 链接")
	}
	if m := urlRe.FindStringSubmatch(raw); m != nil {
		return m[1], nil
	}
	for _, p := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if idRe.MatchString(p) {
			return p, nil
		}
	}
	return "", errs.InvalidURL("无效的 Bilibili 直播链接，示例：https://live.bilibili.com/6")
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

type roomInitData struct {
	RoomID     int64 `json:"room_id"`
	LiveStatus int   `json:"live_status"` // 0 off, 1 live, 2 round
}

type roomInfoData struct {
	Title string `json:"title"`
}

type playInfoData struct {
	PlayURLInfo *struct {
		PlayURL *struct {
			GQnDesc []struct {
				Qn   int    `json:"qn"`
				Desc string `json:"desc"`
			} `json:"g_qn_desc"`
			Stream []streamProto `json:"stream"`
		} `json:"playurl"`
	} `json:"playurl_info"`
}

type streamProto struct {
	ProtocolName string `json:"protocol_name"`
	Format       []struct {
		FormatName string `json:"format_name"`
		Codec      []struct {
			CodecName string `json:"codec_name"`
			BaseURL   string `json:"base_url"`
			AcceptQn  []int  `json:"accept_qn"`
			URLInfo   []struct {
				Host  string `json:"host"`
				Extra string `json:"extra"`
			} `json:"url_info"`
		} `json:"codec"`
	} `json:"format"`
}

func (c *Client) getJSON(ctx context.Context, endpoint string, q url.Values, dest any) error {
	u := endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")
	resp, err := c.http.Do(req)
	if err != nil {
		return errs.WrapResolve(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return errs.WrapResolve(err)
	}
	if resp.StatusCode >= 400 {
		return errs.ResolveFailed(fmt.Sprintf("Bilibili API HTTP %d", resp.StatusCode))
	}
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return errs.ResolveFailed("Bilibili API JSON 解析失败")
	}
	if env.Code != 0 {
		msg := env.Message
		if msg == "" {
			msg = env.Msg
		}
		if msg == "" {
			msg = fmt.Sprintf("code=%d", env.Code)
		}
		if strings.Contains(msg, "不存在") {
			return errs.InvalidURL("Bilibili 直播间不存在")
		}
		return errs.ResolveFailed("Bilibili: " + msg)
	}
	if dest != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, dest); err != nil {
			return errs.ResolveFailed("Bilibili data 解析失败")
		}
	}
	return nil
}

// Resolve extracts multi-quality HLS/FLV URLs for a Bilibili live room.
func (c *Client) Resolve(ctx context.Context, raw string) (*model.Result, error) {
	roomID, err := ParseRoomID(raw)
	if err != nil {
		return nil, err
	}

	var init roomInitData
	if err := c.getJSON(ctx, roomInitAPI, url.Values{"id": {roomID}}, &init); err != nil {
		return nil, err
	}
	realID := roomID
	if init.RoomID != 0 {
		realID = strconv.FormatInt(init.RoomID, 10)
	}
	if init.LiveStatus != 1 {
		return nil, errs.NotLive("该 Bilibili 直播间未开播")
	}

	title := ""
	var info roomInfoData
	if err := c.getJSON(ctx, roomInfoAPI, url.Values{"room_id": {realID}}, &info); err == nil {
		title = info.Title
	}

	first, err := c.fetchPlayInfo(ctx, realID, 10000)
	if err != nil {
		return nil, err
	}
	qnDesc, accept := collectQnMeta(first)
	if len(accept) == 0 {
		accept = []int{10000, 400, 250, 150, 80}
	}
	sort.Slice(accept, func(i, j int) bool { return accept[i] > accept[j] })
	if len(accept) > maxQnFetch {
		accept = accept[:maxQnFetch]
	}

	// Reuse first response for its matching qn (usually 10000 / max).
	byQn := map[int]*playInfoData{10000: first}
	for _, qn := range accept {
		if _, ok := byQn[qn]; ok {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		p, err := c.fetchPlayInfo(ctx, realID, qn)
		if err != nil {
			continue
		}
		byQn[qn] = p
	}

	var qualities []model.Quality
	seen := map[string]bool{}
	for _, qn := range accept {
		p := byQn[qn]
		if p == nil {
			continue
		}
		label := qnDesc[qn]
		if label == "" {
			label = qnLabels[qn]
		}
		if label == "" {
			label = fmt.Sprintf("qn%d", qn)
		}
		hlsURL, flvURL := pickStreams(p)
		if hlsURL != "" {
			name := fmt.Sprintf("qn%d_hls", qn)
			if !seen[name] {
				seen[name] = true
				qualities = append(qualities, model.Quality{
					Label: label + " HLS", Name: name, DirectURL: hlsURL, Protocol: "hls",
				})
			}
		}
		if flvURL != "" {
			name := fmt.Sprintf("qn%d_flv", qn)
			if !seen[name] {
				seen[name] = true
				qualities = append(qualities, model.Quality{
					Label: label + " FLV", Name: name, DirectURL: flvURL, Protocol: "progressive",
				})
			}
		}
	}

	if len(qualities) == 0 {
		return nil, errs.ResolveFailed("未能获取 Bilibili 流地址（可能未开播或接口变更）")
	}
	return &model.Result{
		Channel:   realID,
		BNO:       realID,
		Title:     title,
		Platform:  "bilibili",
		IsLive:    true,
		Qualities: qualities,
	}, nil
}

func collectQnMeta(p *playInfoData) (map[int]string, []int) {
	desc := map[int]string{}
	var accept []int
	if p == nil || p.PlayURLInfo == nil || p.PlayURLInfo.PlayURL == nil {
		return desc, accept
	}
	for _, d := range p.PlayURLInfo.PlayURL.GQnDesc {
		if d.Desc != "" {
			desc[d.Qn] = d.Desc
		}
	}
	seen := map[int]struct{}{}
	for _, st := range p.PlayURLInfo.PlayURL.Stream {
		for _, f := range st.Format {
			for _, codec := range f.Codec {
				for _, q := range codec.AcceptQn {
					if _, ok := seen[q]; ok {
						continue
					}
					seen[q] = struct{}{}
					accept = append(accept, q)
				}
			}
		}
	}
	return desc, accept
}

// pickStreams returns one AVC HLS and one FLV URL if present.
func pickStreams(p *playInfoData) (hlsURL, flvURL string) {
	if p == nil || p.PlayURLInfo == nil || p.PlayURLInfo.PlayURL == nil {
		return "", ""
	}
	type cand struct {
		url, kind, format string // kind: hls|flv
	}
	var list []cand
	for _, st := range p.PlayURLInfo.PlayURL.Stream {
		for _, f := range st.Format {
			for _, codec := range f.Codec {
				if codec.CodecName != "" && codec.CodecName != "avc" && codec.CodecName != "h264" {
					continue
				}
				if len(codec.URLInfo) == 0 || codec.BaseURL == "" {
					continue
				}
				full := strings.TrimRight(codec.URLInfo[0].Host, "/") + codec.BaseURL + codec.URLInfo[0].Extra
				kind := "flv"
				if st.ProtocolName == "http_hls" || f.FormatName == "ts" || f.FormatName == "fmp4" {
					kind = "hls"
				}
				if f.FormatName == "flv" || st.ProtocolName == "http_stream" {
					kind = "flv"
				}
				list = append(list, cand{url: full, kind: kind, format: f.FormatName})
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		score := func(c cand) int {
			if c.kind == "hls" && c.format == "ts" {
				return 0
			}
			if c.kind == "hls" {
				return 1
			}
			return 2
		}
		return score(list[i]) < score(list[j])
	})
	for _, c := range list {
		if c.kind == "hls" && hlsURL == "" {
			hlsURL = c.url
		}
		if c.kind == "flv" && flvURL == "" {
			flvURL = c.url
		}
	}
	return hlsURL, flvURL
}

func (c *Client) fetchPlayInfo(ctx context.Context, roomID string, qn int) (*playInfoData, error) {
	q := url.Values{
		"room_id":  {roomID},
		"protocol": {"0,1"},
		"format":   {"0,1,2"},
		"codec":    {"0,1"},
		"qn":       {strconv.Itoa(qn)},
		"platform": {"web"},
		"ptype":    {"8"},
		"dolby":    {"5"},
		"panorama": {"1"},
	}
	var data playInfoData
	if err := c.getJSON(ctx, playInfoAPI, q, &data); err != nil {
		return nil, err
	}
	return &data, nil
}
