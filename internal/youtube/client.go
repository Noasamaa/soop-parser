package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Noasamaa/soop-parser/internal/errs"
	"github.com/Noasamaa/soop-parser/internal/model"
)

var hostRe = regexp.MustCompile(`(?i)(^|\.)((youtube\.com)|(youtube-nocookie\.com)|(youtu\.be)|(music\.youtube\.com))$`)

// Client resolves YouTube via the yt-dlp CLI.
type Client struct {
	timeout     time.Duration
	cookiesFile string
	ytdlpPath   string
}

func NewClient(timeout time.Duration, cookiesFile string) *Client {
	return &Client{
		timeout:     timeout,
		cookiesFile: cookiesFile,
		ytdlpPath:   "yt-dlp",
	}
}

// IsURL reports whether raw looks like a YouTube URL.
func IsURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if host == "youtu.be" || strings.HasSuffix(host, ".youtu.be") {
		return true
	}
	return hostRe.MatchString(host)
}

type ytdlpFmt struct {
	FormatID string  `json:"format_id"`
	URL      string  `json:"url"`
	Protocol string  `json:"protocol"`
	VCodec   string  `json:"vcodec"`
	ACodec   string  `json:"acodec"`
	Height   int     `json:"height"`
	FPS      float64 `json:"fps"`
	TBR      float64 `json:"tbr"`
	Ext      string  `json:"ext"`
	Note     string  `json:"format_note"`
}

type ytdlpInfo struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Channel    string      `json:"channel"`
	ChannelID  string      `json:"channel_id"`
	Uploader   string      `json:"uploader"`
	UploaderID string      `json:"uploader_id"`
	IsLive     bool        `json:"is_live"`
	LiveStatus string      `json:"live_status"`
	URL        string      `json:"url"`
	Formats    []ytdlpFmt  `json:"formats"`
	Type       string      `json:"_type"`
	Entries    []ytdlpInfo `json:"entries"`
}

func (c *Client) extract(ctx context.Context, raw string) (*ytdlpInfo, error) {
	args := []string{
		"--skip-download",
		"--no-playlist",
		"--no-warnings",
		"-J",
		"--socket-timeout", fmt.Sprintf("%d", int(c.timeout.Seconds())),
		"--js-runtimes", "node",
		"--remote-components", "ejs:github",
	}
	if c.cookiesFile != "" {
		args = append(args, "--cookies", c.cookiesFile)
	}
	args = append(args, raw)

	ctx, cancel := context.WithTimeout(ctx, c.timeout+30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.ytdlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if msg == "" {
			msg = err.Error()
		}
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "sign in") || strings.Contains(low, "login required") ||
			strings.Contains(low, "confirm your age") || strings.Contains(low, "age"):
			return nil, errs.LoginRequired("该 YouTube 视频需要登录/年龄确认。请配置 YOUTUBE_COOKIES_FILE")
		case strings.Contains(low, "not available in your country") || strings.Contains(low, "blocked it in your country"):
			return nil, errs.GeoRestricted("YouTube 地区限制：服务器出口 IP 无法访问该内容")
		case strings.Contains(low, "live event will begin") || strings.Contains(low, "is offline") || strings.Contains(low, "premier"):
			return nil, errs.NotLive("直播未开始或频道离线")
		case strings.Contains(low, "private video") || strings.Contains(low, "video unavailable"):
			return nil, errs.ResolveFailed("视频不可用: " + truncate(msg, 200))
		case strings.Contains(low, "executable file not found"):
			return nil, errs.ResolveFailed("服务器未安装 yt-dlp")
		default:
			return nil, errs.ResolveFailed("yt-dlp 解析失败: " + truncate(msg, 300))
		}
	}

	var info ytdlpInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, errs.ResolveFailed("yt-dlp JSON 解析失败")
	}
	if info.Type == "playlist" && len(info.Entries) > 0 {
		info = info.Entries[0]
	}
	return &info, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func hasVideo(f ytdlpFmt) bool {
	return f.VCodec != "" && f.VCodec != "none"
}

func hasAudio(f ytdlpFmt) bool {
	return f.ACodec != "" && f.ACodec != "none"
}

func isHLS(f ytdlpFmt) bool {
	p := strings.ToLower(f.Protocol)
	return strings.Contains(p, "m3u8") || strings.Contains(f.URL, ".m3u8")
}

func labelOf(f ytdlpFmt, protocol string) string {
	var bits []string
	if f.Height > 0 {
		bits = append(bits, fmt.Sprintf("%dp", f.Height))
	} else if f.Note != "" {
		bits = append(bits, f.Note)
	} else {
		bits = append(bits, f.FormatID)
	}
	if f.FPS >= 48 {
		bits = append(bits, fmt.Sprintf("%dfps", int(f.FPS)))
	}
	if protocol == "hls" {
		bits = append(bits, "HLS")
	} else {
		ext := f.Ext
		if ext == "" {
			ext = "mp4"
		}
		bits = append(bits, strings.ToUpper(ext))
	}
	if f.TBR > 0 {
		bits = append(bits, fmt.Sprintf("%dkbps", int(f.TBR)))
	}
	return strings.Join(bits, " · ")
}

func pickQualities(info *ytdlpInfo) ([]model.Quality, error) {
	var qualities []model.Quality
	seenHLS := map[int]bool{}
	seenProg := map[int]bool{}

	var hls []ytdlpFmt
	for _, f := range info.Formats {
		if f.URL == "" || !isHLS(f) {
			continue
		}
		if hasVideo(f) || f.Height > 0 {
			hls = append(hls, f)
		}
	}
	sort.Slice(hls, func(i, j int) bool {
		if hls[i].Height != hls[j].Height {
			return hls[i].Height > hls[j].Height
		}
		return hls[i].TBR > hls[j].TBR
	})
	for _, f := range hls {
		if f.Height > 0 && seenHLS[f.Height] {
			continue
		}
		if f.Height > 0 {
			seenHLS[f.Height] = true
		}
		qualities = append(qualities, model.Quality{
			Label:     labelOf(f, "hls"),
			Name:      f.FormatID,
			DirectURL: f.URL,
			Protocol:  "hls",
		})
	}

	var prog []ytdlpFmt
	for _, f := range info.Formats {
		if f.URL == "" || isHLS(f) || !hasVideo(f) || !hasAudio(f) {
			continue
		}
		if f.Protocol != "" && !strings.HasPrefix(strings.ToLower(f.Protocol), "http") {
			continue
		}
		prog = append(prog, f)
	}
	sort.Slice(prog, func(i, j int) bool {
		if prog[i].Height != prog[j].Height {
			return prog[i].Height > prog[j].Height
		}
		return prog[i].TBR > prog[j].TBR
	})
	for _, f := range prog {
		if f.Height > 0 && (seenHLS[f.Height] || seenProg[f.Height]) {
			continue
		}
		if f.Height > 0 {
			seenProg[f.Height] = true
		}
		name := f.FormatID
		if name == "" {
			name = "http-" + strconv.Itoa(f.Height)
		}
		qualities = append(qualities, model.Quality{
			Label:     labelOf(f, "progressive"),
			Name:      name,
			DirectURL: f.URL,
			Protocol:  "progressive",
		})
	}

	if len(qualities) == 0 && info.URL != "" {
		proto := "progressive"
		if strings.Contains(info.URL, ".m3u8") {
			proto = "hls"
		}
		qualities = append(qualities, model.Quality{
			Label:     "default",
			Name:      "default",
			DirectURL: info.URL,
			Protocol:  proto,
		})
	}
	if len(qualities) == 0 {
		return nil, errs.ResolveFailed("未找到浏览器可直接播放的格式（需要合并音视频的 HLS/MP4）")
	}

	sort.SliceStable(qualities, func(i, j int) bool {
		hi := heightFromLabel(qualities[i].Label)
		hj := heightFromLabel(qualities[j].Label)
		if hi != hj {
			return hi > hj
		}
		return qualities[i].Protocol == "hls" && qualities[j].Protocol != "hls"
	})
	return qualities, nil
}

func heightFromLabel(label string) int {
	re := regexp.MustCompile(`(\d{3,4})p`)
	m := re.FindStringSubmatch(label)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Resolve extracts playable formats for a YouTube URL.
func (c *Client) Resolve(ctx context.Context, raw string) (*model.Result, error) {
	if !IsURL(raw) {
		return nil, errs.InvalidURL("不是有效的 YouTube 链接")
	}
	info, err := c.extract(ctx, raw)
	if err != nil {
		return nil, err
	}
	if info.LiveStatus == "is_upcoming" {
		return nil, errs.NotLive("直播尚未开始（upcoming）")
	}
	qs, err := pickQualities(info)
	if err != nil {
		return nil, err
	}
	channel := firstNonEmpty(info.ChannelID, info.UploaderID, info.Channel, info.Uploader, "youtube")
	author := firstNonEmpty(info.Channel, info.Uploader)
	isLive := info.IsLive || info.LiveStatus == "is_live" || info.LiveStatus == "post_live"
	return &model.Result{
		Channel:   channel,
		BNO:       firstNonEmpty(info.ID, "youtube"),
		Title:     info.Title,
		Author:    author,
		Platform:  "youtube",
		IsLive:    isLive,
		Qualities: qs,
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
