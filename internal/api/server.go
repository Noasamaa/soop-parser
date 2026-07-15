package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Noasamaa/soop-parser/internal/bilibili"
	"github.com/Noasamaa/soop-parser/internal/catalog"
	"github.com/Noasamaa/soop-parser/internal/config"
	"github.com/Noasamaa/soop-parser/internal/errs"
	"github.com/Noasamaa/soop-parser/internal/hupu"
	"github.com/Noasamaa/soop-parser/internal/huya"
	"github.com/Noasamaa/soop-parser/internal/model"
	"github.com/Noasamaa/soop-parser/internal/proxy"
	"github.com/Noasamaa/soop-parser/internal/schedule"
	"github.com/Noasamaa/soop-parser/internal/session"
	"github.com/Noasamaa/soop-parser/internal/soop"
	"github.com/Noasamaa/soop-parser/internal/youtube"
)

// Server is the HTTP API + static UI.
type Server struct {
	cfg      config.Config
	sessions *session.Store
	soop     *soop.Client
	youtube  *youtube.Client
	bilibili *bilibili.Client
	huya     *huya.Client
	catalog  *catalog.Service
	schedule *schedule.Service
	hupu     *hupu.Service
	upstream *http.Client
	static   http.Handler
	mux      *http.ServeMux
}

func New(cfg config.Config, staticFS http.FileSystem) *Server {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        cfg.MaxUpstreamConns,
		MaxIdleConnsPerHost: cfg.MaxUpstreamConns / 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{
		Timeout:   cfg.HTTPTimeout,
		Transport: transport,
	}
	// Streaming client without overall Timeout (use context per request)
	streamClient := &http.Client{
		Transport: transport,
	}

	s := &Server{
		cfg:      cfg,
		sessions: session.NewStore(cfg.PlayTokenTTL, cfg.MaxSessions),
		soop:     soop.NewClient(client, cfg.SOOPUsername, cfg.SOOPPassword),
		youtube:  youtube.NewClient(cfg.HTTPTimeout, cfg.YouTubeCookiesFile),
		bilibili: bilibili.NewClient(client),
		huya:     huya.NewClient(client),
		catalog:  catalog.New(client),
		schedule: schedule.New(client),
		hupu:     hupu.New(client),
		upstream: streamClient,
		mux:      http.NewServeMux(),
	}
	if staticFS != nil {
		s.static = http.FileServer(staticFS)
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("HEAD /health", s.handleHealth)
	s.mux.HandleFunc("GET /api/config", s.withAuth(s.handleConfig))
	s.mux.HandleFunc("HEAD /api/config", s.withAuth(s.handleConfig))
	s.mux.HandleFunc("GET /api/catalog", s.withAuth(s.handleCatalog))
	s.mux.HandleFunc("HEAD /api/catalog", s.withAuth(s.handleCatalog))
	s.mux.HandleFunc("GET /api/schedule", s.withAuth(s.handleSchedule))
	s.mux.HandleFunc("HEAD /api/schedule", s.withAuth(s.handleSchedule))
	s.mux.HandleFunc("GET /api/hupu/rating", s.withAuth(s.handleHupuRating))
	s.mux.HandleFunc("POST /api/resolve", s.withAuth(s.handleResolve))
	s.mux.HandleFunc("GET /api/hls/{token}/playlist.m3u8", s.withAuth(s.handlePlaylist))
	s.mux.HandleFunc("HEAD /api/hls/{token}/playlist.m3u8", s.withAuth(s.handlePlaylistHEAD))
	s.mux.HandleFunc("GET /api/hls/{token}/proxy", s.withAuth(s.handleSegment))
	s.mux.HandleFunc("HEAD /api/hls/{token}/proxy", s.withAuth(s.handleSegmentHEAD))
	s.mux.HandleFunc("GET /api/media/{token}", s.withAuth(s.handleMedia))
	s.mux.HandleFunc("HEAD /api/media/{token}", s.withAuth(s.handleMediaHEAD))
	if s.static != nil {
		// No method prefix: avoids Go 1.22 ServeMux conflict with method-specific
		// more-specific routes (HEAD / vs GET /health).
		s.mux.Handle("/", s.static)
	}
}

// Handler returns the root handler with CORS.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Access-Token")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok := strings.TrimSpace(s.cfg.AccessToken); tok != "" {
			provided := strings.TrimSpace(r.Header.Get("X-Access-Token"))
			if provided == "" {
				provided = strings.TrimSpace(r.URL.Query().Get("token"))
			}
			if provided == "" {
				if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
					provided = strings.TrimSpace(auth[7:])
				}
			}
			if provided != tok {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"ok": false, "code": "unauthorized", "message": "无效的访问令牌",
				})
				return
			}
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *errs.AppError
	if errors.As(err, &ae) {
		writeJSON(w, ae.StatusCode, map[string]any{
			"ok": false, "code": ae.Code, "message": ae.Message,
		})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"ok": false, "code": "resolve_failed", "message": err.Error(),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service": "live-parser", "platforms": supportedPlatforms(), "engine": "go",
	})
}

func supportedPlatforms() []string {
	return []string{"soop", "youtube", "bilibili", "huya"}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_required":              s.cfg.AccessToken != "",
		"login_configured":           s.cfg.SOOPUsername != "" && s.cfg.SOOPPassword != "",
		"youtube_cookies_configured": s.cfg.YouTubeCookiesFile != "",
		"play_token_ttl":             int(s.cfg.PlayTokenTTL.Seconds()),
		"platforms":                  supportedPlatforms(),
		"public_base_url":            s.publicBase(r),
		"engine":                     "go",
		"yt_dlp_hint":                "yt-dlp CLI + node (EJS)",
		"cn_direct_default":          true,
		"presets":                    defaultPresets(),
	})
}

// defaultPresets are Taiwan LoL Chinese commentary shortcuts (legacy pill UI).
func defaultPresets() []map[string]any {
	return []map[string]any{
		{
			"id": "loltw", "label": "台灣中文", "badge": "SOOP", "primary": true,
			"url":  "https://play.sooplive.com/loltw",
			"note": "SOOP 台灣中文賽事轉播（EWC / 國際賽等）",
		},
		{
			"id": "lckcarry", "label": "LCK 中文", "badge": "SOOP",
			"url":  "https://play.sooplive.com/lckcarry",
			"note": "LCK 中文轉播（鍇睿 / SOOP）",
		},
		{
			"id": "carrylck", "label": "LCK 中文·備", "badge": "SOOP",
			"url":  "https://play.sooplive.com/carrylck",
			"note": "LCK 中文備用頻道 ID",
		},
		{
			"id": "yt-lckcarry", "label": "LCK-Carry", "badge": "YT",
			"url":  "https://www.youtube.com/@LCKCarry/live",
			"note": "YouTube LCK 中文（有直播時）",
		},
		{
			"id": "yt-lcp", "label": "LCP / 太平洋", "badge": "YT",
			"url":  "https://www.youtube.com/@lolesportstw/live",
			"note": "YouTube LoLEsportsTW / LCP 官方",
		},
	}
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.catalog.Get(ctx)
	if err != nil {
		writeErr(w, errs.ResolveFailed("赛事目录加载失败: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"updated_at": res.UpdatedAt,
		"groups":     res.Groups,
	})
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.schedule.Get(ctx)
	if err != nil {
		writeErr(w, errs.ResolveFailed("赛程加载失败: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"updated_at":  res.UpdatedAt,
		"tournaments": res.Tournaments,
		"note":        "整届赛事 endDate 后 3 天自动隐藏（非单场结束）",
	})
}

func (s *Server) handleHupuRating(w http.ResponseWriter, r *http.Request) {
	teamA := strings.TrimSpace(r.URL.Query().Get("team_a"))
	teamB := strings.TrimSpace(r.URL.Query().Get("team_b"))
	matchID := strings.TrimSpace(r.URL.Query().Get("match_id"))
	if matchID == "" && (teamA == "" || teamB == "") {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "code": "bad_request",
			"message": "需要 team_a+team_b 或 match_id",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var (
		rating *hupu.MatchRating
		err    error
	)
	if matchID != "" {
		rating, err = s.hupu.RatingByMatchID(ctx, matchID)
	} else {
		rating, err = s.hupu.RatingForTeams(ctx, teamA, teamB)
	}
	if err != nil {
		// still return structured payload when possible
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"available": false,
			"message":   err.Error(),
			"team_a":    teamA,
			"team_b":    teamB,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"rating": rating,
	})
}

func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.PublicBaseURL != "" {
		return s.cfg.PublicBaseURL
	}
	proto := firstHeader(r, "X-Forwarded-Proto")
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := firstHeader(r, "X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

func firstHeader(r *http.Request, key string) string {
	v := r.Header.Get(key)
	if v == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(v, ",")[0])
}

type resolveReq struct {
	URL            string `json:"url"`
	StreamPassword string `json:"stream_password"`
	Proxy          *bool  `json:"proxy"`
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "code": "bad_request", "message": "无效 JSON"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.HTTPTimeout+60*time.Second)
	defer cancel()

	raw := strings.TrimSpace(req.URL)
	var res *model.Result
	var err error

	switch {
	case youtube.IsURL(raw):
		res, err = s.youtube.Resolve(ctx, raw)
	case isSOOPURL(raw):
		res, err = s.soop.Resolve(ctx, raw, req.StreamPassword)
	case bilibili.IsURL(raw):
		res, err = s.bilibili.Resolve(ctx, raw)
	case huya.IsURL(raw):
		res, err = s.huya.Resolve(ctx, raw)
	default:
		writeErr(w, errs.InvalidURL("无法识别链接。支持 SOOP / YouTube / Bilibili / 虎牙"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// SOOP / YouTube default to proxy (geo). CN platforms always direct:
	// their CDNs are not on the proxy allowlist and should not use server bandwidth.
	useProxy := !isCNDirectPlatform(res.Platform)
	if req.Proxy != nil && !isCNDirectPlatform(res.Platform) {
		useProxy = *req.Proxy
	}

	base := s.publicBase(r)
	outQ := make([]model.Quality, 0, len(res.Qualities))
	for _, q := range res.Qualities {
		item := q
		if useProxy && q.DirectURL != "" {
			mt := session.MediaHLS
			if q.Protocol == "progressive" {
				mt = session.MediaProgressive
			}
			sess := s.sessions.Create(q.DirectURL, res.Platform, res.Channel, q.Name, q.Label, mt)
			if mt == session.MediaProgressive {
				item.PlayURL = s.withToken(base + "/api/media/" + sess.Token)
			} else {
				item.PlayURL = s.withToken(base + "/api/hls/" + sess.Token + "/playlist.m3u8")
			}
			item.DirectURL = ""
		} else {
			item.PlayURL = q.DirectURL
		}
		outQ = append(outQ, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"platform":           res.Platform,
		"is_live":            res.IsLive,
		"channel":            res.Channel,
		"bno":                res.BNO,
		"title":              res.Title,
		"author":             res.Author,
		"password_protected": res.PasswordProtected,
		"proxied":            useProxy,
		"qualities":          outQ,
	})
}

func isCNDirectPlatform(platform string) bool {
	switch platform {
	case "bilibili", "huya":
		return true
	default:
		return false
	}
}

func isSOOPURL(raw string) bool {
	_, _, err := soop.ParseURL(raw)
	return err == nil
}

func (s *Server) withToken(u string) string {
	if s.cfg.AccessToken == "" {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "token=" + url.QueryEscape(s.cfg.AccessToken)
}

func (s *Server) proxyBase(r *http.Request, token string) string {
	base := s.publicBase(r) + "/api/hls/" + token + "/proxy?"
	if s.cfg.AccessToken != "" {
		base += "token=" + url.QueryEscape(s.cfg.AccessToken) + "&"
	}
	return base + "u="
}

func (s *Server) handlePlaylistHEAD(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sess := s.sessions.Get(token)
	if sess == nil {
		http.Error(w, "session expired", http.StatusNotFound)
		return
	}
	if sess.MediaType != session.MediaHLS {
		http.Error(w, "not hls", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sess := s.sessions.Get(token)
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "code": "session_expired", "message": "播放会话不存在或已过期，请重新解析"})
		return
	}
	if sess.MediaType != session.MediaHLS {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "该会话是 progressive 媒体"})
		return
	}
	if !proxy.IsAllowedUpstream(sess.UpstreamURL, sess.Platform) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "上游地址不被允许"})
		return
	}

	body, status, err := s.fetchBytes(r.Context(), sess.UpstreamURL, sess.Platform, sess.Channel)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": "拉取 playlist 失败: " + err.Error()})
		return
	}
	if status >= 400 {
		code := http.StatusBadGateway
		if status == 403 || status == 451 {
			code = 451
		}
		writeJSON(w, code, map[string]any{"ok": false, "message": "上游 playlist HTTP " + strconv.Itoa(status)})
		return
	}

	rewritten := proxy.RewriteM3U8(string(body), sess.UpstreamURL, s.proxyBase(r, token))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(rewritten))
}

func (s *Server) handleSegmentHEAD(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sess := s.sessions.Get(token)
	if sess == nil {
		http.Error(w, "session expired", http.StatusNotFound)
		return
	}
	raw := r.URL.Query().Get("u")
	// Query().Get unescapes once; do not Unescape again (breaks signed CDN URLs).
	if raw == "" || !proxy.AllowedForSession(raw, sess.Platform, sess.UpstreamHost) {
		http.Error(w, "bad upstream", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", proxy.GuessMediaType(raw))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sess := s.sessions.Get(token)
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "播放会话不存在或已过期"})
		return
	}
	// Query().Get unescapes once; do not Unescape again (breaks signed CDN URLs).
	raw := r.URL.Query().Get("u")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "缺少 u 参数"})
		return
	}
	if !proxy.AllowedForSession(raw, sess.Platform, sess.UpstreamHost) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "上游 host 不在本会话允许范围"})
		return
	}

	if proxy.IsHLSPlaylistURL(raw) {
		body, status, err := s.fetchBytes(r.Context(), raw, sess.Platform, sess.Channel)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		if status >= 400 {
			http.Error(w, "upstream playlist error", status)
			return
		}
		rewritten := proxy.RewriteM3U8(string(body), raw, s.proxyBase(r, token))
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(rewritten))
		return
	}

	s.streamUpstream(w, r, raw, sess.Platform, sess.Channel)
}

func (s *Server) handleMediaHEAD(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sess := s.sessions.Get(token)
	if sess == nil {
		http.Error(w, "session expired", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", proxy.GuessMediaType(sess.UpstreamURL))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sess := s.sessions.Get(token)
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "播放会话不存在或已过期"})
		return
	}
	if !proxy.IsAllowedUpstream(sess.UpstreamURL, sess.Platform) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "上游地址不被允许"})
		return
	}
	s.streamUpstream(w, r, sess.UpstreamURL, sess.Platform, sess.Channel)
}

func (s *Server) streamUpstream(w http.ResponseWriter, r *http.Request, raw, platform, channel string) {
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range proxy.UpstreamHeaders(platform, channel) {
		req.Header.Set(k, v)
	}
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}

	resp, err := s.upstream.Do(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// Real status — do not pretend 200 empty body
		http.Error(w, "upstream HTTP "+strconv.Itoa(resp.StatusCode), resp.StatusCode)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = proxy.GuessMediaType(raw)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	if ar := resp.Header.Get("Accept-Ranges"); ar != "" {
		w.Header().Set("Accept-Ranges", ar)
	} else {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) fetchBytes(ctx context.Context, raw, platform, channel string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range proxy.UpstreamHeaders(platform, channel) {
		req.Header.Set(k, v)
	}
	resp, err := s.upstream.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, err
}

// LogRequest is a tiny middleware logger.
func LogRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/health" {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
