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
	// live.bilibili.com/123 / h5/123 / blanc/123
	urlRe = regexp.MustCompile(`(?i)(?:live\.bilibili\.com/(?:h5/|blanc/)?)(\d+)`)
	// bare room id
	idRe = regexp.MustCompile(`^\d{1,12}$`)
)

// Common qn labels used by Bilibili live.
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
	ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

	roomInitAPI = "https://api.live.bilibili.com/room/v1/Room/room_init"
	playInfoAPI = "https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo"
	roomInfoAPI = "https://api.live.bilibili.com/room/v1/Room/get_info"
)

// Client talks to Bilibili live APIs (resolve-only; no proxy needed for CN CDN).
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

// IsURL reports whether raw looks like a Bilibili live link or room id.
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
	// allow bare path-like "live.bilibili.com/123" without scheme
	if !strings.Contains(raw, "://") && strings.Contains(strings.ToLower(raw), "bilibili") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errs.InvalidURL("无效的 Bilibili 链接")
	}
	host := strings.ToLower(u.Host)
	if host != "" && !strings.Contains(host, "bilibili.com") {
		return "", errs.InvalidURL("无效的 Bilibili 链接")
	}
	if m := urlRe.FindStringSubmatch(raw); m != nil {
		return m[1], nil
	}
	// path segments: /123 or /h5/123
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if idRe.MatchString(parts[i]) {
			return parts[i], nil
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

func (c *Client) getJSON(ctx context.Context, endpoint string, q url.Values, dest any) error {
	u := endpoint
	if len(q) > 0 {
		u = endpoint + "?" + q.Encode()
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
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

type roomInitData struct {
	RoomID     int64 `json:"room_id"`
	ShortID    int64 `json:"short_id"`
	UID        int64 `json:"uid"`
	LiveStatus int   `json:"live_status"` // 0 off, 1 live, 2 round
}

type roomInfoData struct {
	RoomID     int64  `json:"room_id"`
	Title      string `json:"title"`
	UID        int64  `json:"uid"`
	LiveStatus int    `json:"live_status"`
	Uname      string `json:"uname"` // sometimes empty; filled via other field
}

type playInfoData struct {
	RoomID      int64 `json:"room_id"`
	LiveStatus  int   `json:"live_status"`
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
	ProtocolName string `json:"protocol_name"` // http_stream | http_hls
	Format       []struct {
		FormatName string `json:"format_name"` // flv | ts | fmp4
		Codec      []struct {
			CodecName string `json:"codec_name"` // avc | hevc
			BaseURL   string `json:"base_url"`
			CurrentQn int    `json:"current_qn"`
			AcceptQn  []int  `json:"accept_qn"`
			URLInfo   []struct {
				Host  string `json:"host"`
				Extra string `json:"extra"`
			} `json:"url_info"`
		} `json:"codec"`
	} `json:"format"`
}

// Resolve extracts multi-quality FLV/HLS URLs for a Bilibili live room.
func (c *Client) Resolve(ctx context.Context, raw string) (*model.Result, error) {
	roomID, err := ParseRoomID(raw)
	if err != nil {
		return nil, err
	}

	var init roomInitData
	if err := c.getJSON(ctx, roomInitAPI, url.Values{"id": {roomID}}, &init); err != nil {
		return nil, err
	}
	realID := strconv.FormatInt(init.RoomID, 10)
	if init.RoomID == 0 {
		realID = roomID
	}
	if init.LiveStatus != 1 {
		return nil, errs.NotLive("该 Bilibili 直播间未开播")
	}

	title, author := "", ""
	var info roomInfoData
	if err := c.getJSON(ctx, roomInfoAPI, url.Values{"room_id": {realID}}, &info); err == nil {
		title = info.Title
	}

	// First probe to get accept_qn + g_qn_desc
	play, err := c.fetchPlayInfo(ctx, realID, 10000)
	if err != nil {
		return nil, err
	}
	if play.LiveStatus != 0 && play.LiveStatus != 1 {
		// some responses omit or use same as room
	}

	qnDesc := map[int]string{}
	var accept []int
	if play.PlayURLInfo != nil && play.PlayURLInfo.PlayURL != nil {
		for _, d := range play.PlayURLInfo.PlayURL.GQnDesc {
			if d.Desc != "" {
				qnDesc[d.Qn] = d.Desc
			}
		}
		for _, st := range play.PlayURLInfo.PlayURL.Stream {
			for _, f := range st.Format {
				for _, codec := range f.Codec {
					for _, q := range codec.AcceptQn {
						accept = append(accept, q)
					}
				}
			}
		}
	}
	accept = uniqueInts(accept)
	if len(accept) == 0 {
		accept = []int{10000, 400, 250, 150, 80}
	}
	// high quality first
	sort.Slice(accept, func(i, j int) bool { return accept[i] > accept[j] })

	type seenKey struct {
		qn       int
		protocol string
	}
	seen := map[seenKey]bool{}
	var qualities []model.Quality

	// try each qn; prefer HLS (ts) then FLV for browser / PotPlayer
	for _, qn := range accept {
		p, err := c.fetchPlayInfo(ctx, realID, qn)
		if err != nil || p.PlayURLInfo == nil || p.PlayURLInfo.PlayURL == nil {
			continue
		}
		label := qnDesc[qn]
		if label == "" {
			label = qnLabels[qn]
		}
		if label == "" {
			label = fmt.Sprintf("qn%d", qn)
		}

		// collect candidates: protocol priority hls first for same qn
		type cand struct {
			url      string
			protocol string // hls | progressive
			format   string
			codec    string
		}
		var cands []cand
		for _, st := range p.PlayURLInfo.PlayURL.Stream {
			for _, f := range st.Format {
				for _, codec := range f.Codec {
					// prefer avc for max player compatibility
					if codec.CodecName != "" && codec.CodecName != "avc" && codec.CodecName != "h264" {
						continue
					}
					if len(codec.URLInfo) == 0 || codec.BaseURL == "" {
						continue
					}
					host := codec.URLInfo[0].Host
					extra := codec.URLInfo[0].Extra
					full := strings.TrimRight(host, "/") + codec.BaseURL + extra
					proto := "progressive"
					if st.ProtocolName == "http_hls" || f.FormatName == "ts" || f.FormatName == "fmp4" {
						proto = "hls"
					}
					if f.FormatName == "flv" || st.ProtocolName == "http_stream" {
						proto = "progressive"
					}
					cands = append(cands, cand{url: full, protocol: proto, format: f.FormatName, codec: codec.CodecName})
				}
			}
		}
		// order: hls/ts, hls/fmp4, flv
		sort.SliceStable(cands, func(i, j int) bool {
			score := func(c cand) int {
				if c.protocol == "hls" && c.format == "ts" {
					return 0
				}
				if c.protocol == "hls" {
					return 1
				}
				return 2
			}
			return score(cands[i]) < score(cands[j])
		})

		// take one HLS + one FLV per qn if available
		gotHLS, gotFLV := false, false
		for _, cd := range cands {
			if cd.protocol == "hls" {
				if gotHLS {
					continue
				}
				key := seenKey{qn: qn, protocol: "hls"}
				if seen[key] {
					continue
				}
				seen[key] = true
				gotHLS = true
				name := fmt.Sprintf("qn%d_hls", qn)
				qualities = append(qualities, model.Quality{
					Label:     label + " HLS",
					Name:      name,
					DirectURL: cd.url,
					Protocol:  "hls",
				})
			} else {
				if gotFLV {
					continue
				}
				key := seenKey{qn: qn, protocol: "flv"}
				if seen[key] {
					continue
				}
				seen[key] = true
				gotFLV = true
				name := fmt.Sprintf("qn%d_flv", qn)
				qualities = append(qualities, model.Quality{
					Label:     label + " FLV",
					Name:      name,
					DirectURL: cd.url,
					Protocol:  "progressive",
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
		Author:    author,
		Platform:  "bilibili",
		IsLive:    true,
		Qualities: qualities,
	}, nil
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

func uniqueInts(in []int) []int {
	m := map[int]struct{}{}
	var out []int
	for _, v := range in {
		if _, ok := m[v]; ok {
			continue
		}
		m[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
