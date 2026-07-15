package soop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Noasamaa/soop-parser/internal/errs"
	"github.com/Noasamaa/soop-parser/internal/model"
	"github.com/Noasamaa/soop-parser/internal/proxy"
)

var (
	urlRe = regexp.MustCompile(`(?i)^https?://play\.(sooplive\.com|sooplive\.co\.kr|afreecatv\.com)/([\w-]+)(?:/(\d+))?`)
	bnoRe = regexp.MustCompile(`window\.nBroadNo\s*=\s*(\d+);`)
)

const (
	channelAPI = "https://live.sooplive.com/afreeca/player_live_api.php"
	loginURL   = "https://login.sooplive.com/app/LoginAction.php"

	resultOK            = 1
	resultLoginRequired = -6
	pwdProtected        = "Y"
)

var cdnMap = map[string]string{
	"gs_cdn": "gs_cdn_pc_web",
	"lg_cdn": "lg_cdn_pc_web",
}

// Client talks to SOOP player APIs. Owns its http.Client (does not mutate callers).
type Client struct {
	http     *http.Client
	username string
	password string
	loggedIn bool
	mu       sync.Mutex
}

// NewClient builds a dedicated client. base may be nil; Transport/Timeout are copied if set.
func NewClient(base *http.Client, username, password string) *Client {
	var transport http.RoundTripper = http.DefaultTransport
	timeout := 45 * time.Second
	if base != nil {
		if base.Transport != nil {
			transport = base.Transport
		}
		if base.Timeout > 0 {
			timeout = base.Timeout
		}
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			Jar:       jar,
		},
		username: username,
		password: password,
	}
}

// ParseURL returns channel and optional bno.
func ParseURL(raw string) (channel, bno string, err error) {
	raw = strings.TrimSpace(raw)
	m := urlRe.FindStringSubmatch(raw)
	if m == nil {
		return "", "", errs.InvalidURL("无效的 SOOP 直播链接，示例：https://play.sooplive.com/channel/123456")
	}
	return m[2], m[3], nil
}

func mapCDN(cdn string) string {
	for k, v := range cdnMap {
		if strings.Contains(cdn, k) {
			return v
		}
	}
	if cdn == "" {
		return "gs_cdn_pc_web"
	}
	return cdn
}

func appendQuery(raw, key, val string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set(key, val)
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) ensureLogin(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn || c.username == "" || c.password == "" {
		return
	}
	form := url.Values{
		"szWork":        {"login"},
		"szType":        {"json"},
		"szUid":         {c.username},
		"szPassword":    {c.password},
		"isSaveId":      {"true"},
		"isSavePw":      {"false"},
		"isSaveJoin":    {"false"},
		"isLoginRetain": {"Y"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var data struct {
		RESULT int `json:"RESULT"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	c.loggedIn = data.RESULT == 1
}

func (c *Client) channelAPI(ctx context.Context, form url.Values, referer string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channelAPI, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", referer)
	req.Header.Set("Origin", "https://play.sooplive.com")
	req.Header.Set("User-Agent", proxy.DefaultUA())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errs.ResolveFailed("SOOP API JSON 解析失败")
	}
	ch, _ := payload["CHANNEL"].(map[string]any)
	if ch == nil {
		return nil, errs.ResolveFailed("SOOP API 返回异常（无 CHANNEL）")
	}
	return ch, nil
}

func commonForm() url.Values {
	return url.Values{
		"from_api":    {"0"},
		"mode":        {"landing"},
		"player_type": {"html5"},
		"stream_type": {"common"},
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func (c *Client) raiseResult(result int, bpwd string) error {
	switch {
	case result == resultOK:
		return nil
	case result == resultLoginRequired:
		return errs.LoginRequired("该直播需要登录后才能观看，请配置 SOOP_USERNAME / SOOP_PASSWORD")
	case result == -3 || result == -16 || result == -17:
		return errs.GeoRestricted("疑似版权/地区限制（服务器出口 IP 不被允许）")
	case bpwd == pwdProtected:
		return errs.PasswordRequired("该直播有密码保护，请提供 stream_password")
	case result == 0 || result == -1:
		return errs.NotLive("主播未开播或场次无效")
	default:
		return errs.ResolveFailed(fmt.Sprintf("SOOP 返回错误码 RESULT=%d", result))
	}
}

func (c *Client) fetchBNO(ctx context.Context, channel string) (string, error) {
	page := "https://play.sooplive.com/" + channel
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Referer", page)
	req.Header.Set("User-Agent", proxy.DefaultUA())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	m := bnoRe.FindSubmatch(body)
	if m == nil {
		return "", errs.NotLive("未找到场次号，主播可能未开播")
	}
	return string(m[1]), nil
}

func (c *Client) getAID(ctx context.Context, channel, bno, quality, pwd string) (string, error) {
	form := commonForm()
	form.Set("type", "aid")
	form.Set("bid", channel)
	form.Set("bno", bno)
	form.Set("pwd", pwd)
	form.Set("quality", quality)
	ref := fmt.Sprintf("https://play.sooplive.com/%s/%s", channel, bno)
	ch, err := c.channelAPI(ctx, form, ref)
	if err != nil {
		return "", err
	}
	if asInt(ch["RESULT"]) != resultOK {
		return "", nil
	}
	return asString(ch["AID"]), nil
}

func (c *Client) getViewURL(ctx context.Context, rmd, cdn, bno, quality string) (string, error) {
	rmd = strings.TrimRight(rmd, "/")
	u := rmd + "/broad_stream_assign.html"
	q := url.Values{
		"return_type": {mapCDN(cdn)},
		"broad_key":   {fmt.Sprintf("%s-common-%s-hls", bno, quality)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Referer", "https://play.sooplive.com/")
	req.Header.Set("User-Agent", proxy.DefaultUA())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var data struct {
		ViewURL string `json:"view_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.ViewURL, nil
}

// Resolve extracts multi-quality HLS URLs for a SOOP live URL.
func (c *Client) Resolve(ctx context.Context, rawURL, streamPassword string) (*model.Result, error) {
	channel, bno, err := ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	c.ensureLogin(ctx)
	if bno == "" {
		bno, err = c.fetchBNO(ctx, channel)
		if err != nil {
			return nil, err
		}
	}

	form := commonForm()
	form.Set("type", "live")
	form.Set("bid", channel)
	form.Set("bno", bno)
	form.Set("pwd", streamPassword)
	ref := fmt.Sprintf("https://play.sooplive.com/%s/%s", channel, bno)
	ch, err := c.channelAPI(ctx, form, ref)
	if err != nil {
		return nil, err
	}
	result := asInt(ch["RESULT"])
	bpwd := asString(ch["BPWD"])
	if err := c.raiseResult(result, bpwd); err != nil {
		return nil, err
	}
	resolvedBNO := asString(ch["BNO"])
	if resolvedBNO == "" {
		resolvedBNO = bno
	}
	rmd := asString(ch["RMD"])
	cdn := asString(ch["CDN"])
	if rmd == "" {
		return nil, errs.GeoRestricted("未能获取流媒体节点（RMD 为空）。常见原因：服务器 IP 版权限制、未开播、或需登录。")
	}

	presets, _ := ch["VIEWPRESET"].([]any)
	type job struct {
		name, label string
	}
	var jobs []job
	for _, p := range presets {
		m, _ := p.(map[string]any)
		if m == nil {
			continue
		}
		name := asString(m["name"])
		if name == "" || name == "auto" {
			continue
		}
		label := asString(m["label"])
		if label == "" {
			label = name
		}
		jobs = append(jobs, job{name: name, label: label})
	}

	type qres struct {
		q  model.Quality
		ok bool
	}
	out := make([]qres, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			aid, err := c.getAID(ctx, channel, resolvedBNO, j.name, streamPassword)
			if err != nil || aid == "" {
				return
			}
			view, err := c.getViewURL(ctx, rmd, cdn, resolvedBNO, j.name)
			if err != nil || view == "" {
				return
			}
			out[i] = qres{ok: true, q: model.Quality{
				Label:     j.label,
				Name:      j.name,
				DirectURL: appendQuery(view, "aid", aid),
				Protocol:  "hls",
			}}
		}(i, j)
	}
	wg.Wait()

	var qualities []model.Quality
	for _, r := range out {
		if r.ok {
			qualities = append(qualities, r.q)
		}
	}
	if len(qualities) == 0 {
		if bpwd == pwdProtected && streamPassword == "" {
			return nil, errs.PasswordRequired("该直播有密码保护，请提供 stream_password")
		}
		return nil, errs.ResolveFailed("未能获取任何清晰度。可能原因：版权地区限制、CDN 拒绝、或直播刚结束。")
	}

	return &model.Result{
		Channel:           channel,
		BNO:               resolvedBNO,
		Title:             asString(ch["TITLE"]),
		Author:            asString(ch["BJNICK"]),
		PasswordProtected: bpwd == pwdProtected,
		Platform:          "soop",
		IsLive:            true,
		Qualities:         qualities,
	}, nil
}
