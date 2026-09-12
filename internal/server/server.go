// Package server 承载 HTTP 服务：路由、中间件、鉴权判定树、限速与 handlers。
// CORS 红线：全链路无任何 Access-Control-* 输出（CSRF 防线①）。
package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RexKang/QuotaClock/internal/collector"
	"github.com/RexKang/QuotaClock/internal/config"
	"github.com/RexKang/QuotaClock/internal/crypto"
	"github.com/RexKang/QuotaClock/internal/logx"
	"github.com/RexKang/QuotaClock/internal/persist"

	"golang.org/x/crypto/bcrypt"
)

// SessionCookieName 管理员会话 cookie 名。
const SessionCookieName = "qc_session"

// maxBodyBytes 请求体上限。
const maxBodyBytes = 4 << 20

// Options 装配参数。
type Options struct {
	Version    string
	ConfigPath string
	MasterKey  []byte // token 密封密钥（key.bin）
	SessionKey []byte // HKDF 派生的会话签名密钥

	Store  *collector.Store  // 快照仓库
	Static []byte            // 内嵌静态页
	Icons  map[string][]byte // 内嵌图标（文件名 → 内容，白名单见 web.IconNames）

	// OnConfigChanged 配置保存成功后的热生效钩子（main 注入 scheduler.Reload）。
	OnConfigChanged func(*config.Runtime)

	// FlagOverrides 启动参数对 config 的覆盖（flag > config 优先级在 PUT 热生效后仍保持）。
	FlagOverrides FlagOverrides
}

// FlagOverrides -interval/-addr/-port 覆盖值（零值 = 未覆盖）。
type FlagOverrides struct {
	IntervalBaseS int
	Host          string
	Port          int
}

// Server HTTP 服务。
type Server struct {
	version    string
	configPath string
	key        []byte
	static     []byte
	onChanged  func(*config.Runtime)
	overrides  FlagOverrides

	cfg      atomic.Pointer[config.Runtime]    // 运行时视图（鉴权/采集判据）
	file     atomic.Pointer[config.File]       // 落盘视图（GET /api/config 来源、PUT 合并基础）
	masks    atomic.Pointer[map[string]string] // 掩码 hint（D-5）
	pwDef    atomic.Bool                       // 默认密码未修改检测结果缓存
	sessions *Sessions
	limiter  *Limiter
	client   *collector.Client
	store    *collector.Store
	icons    map[string][]byte
}

// New 装配服务。
func New(opt Options, initial *config.File, runtime *config.Runtime) *Server {
	s := &Server{
		version:    opt.Version,
		configPath: opt.ConfigPath,
		key:        opt.MasterKey,
		static:     opt.Static,
		onChanged:  opt.OnConfigChanged,
		overrides:  opt.FlagOverrides,
		sessions:   NewSessions(opt.SessionKey),
		limiter:    NewLimiter(),
		client:     collector.NewClient(opt.Version),
		store:      opt.Store,
		icons:      opt.Icons,
	}
	s.cfg.Store(runtime)
	s.file.Store(initial)
	s.masks.Store(maskMap(initial, runtime))
	s.pwDef.Store(config.IsDefaultPassword(initial.Auth.PasswordHash))
	return s
}

func maskMap(f *config.File, r *config.Runtime) *map[string]string {
	m := map[string]string{}
	if r == nil {
		return &m
	}
	for _, p := range r.Providers {
		if p.TokenMasked != "" {
			m[p.ID] = p.TokenMasked
		}
	}
	return &m
}

// Handler 返回完整中间件链：recover → security headers → 路由（按权限矩阵）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /icons/{file}", s.handleIcon)
	mux.HandleFunc("GET /api/quotas", s.handleQuotas)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.requireWrite(s.handlePutConfig))
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("POST /api/test", s.requireWrite(s.handleTest))
	return s.recoverMW(s.securityMW(mux))
}

// recoverMW panic → ERROR 日志（含堆栈）+ 统一 500 INTERNAL_ERROR。
func (s *Server) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logx.Errorf("panic %s %s: %v", r.Method, r.URL.Path, rec)
				writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityMW 安全响应头；/api/* 一律 no-store（响应随登录态变化，禁缓存）。
// 零 CORS 头红线：此处刻意不发送任何 Access-Control-* 头。
func (s *Server) securityMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// requireWrite 权限矩阵：admin 模式写操作（PUT config / POST test）须登录；none 模式开放。
func (s *Server) requireWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Load().IsAdmin() {
			next(w, r)
			return
		}
		if _, code := s.checkSession(r); code != "" {
			drainBody(r) // 先读干 body，否则连接被强关、客户端可能丢失这个 401
			writeAuthErr(w, code)
			return
		}
		next(w, r)
	}
}

// checkSession 鉴权判定树（设计 §8.3）：
// 无 cookie/格式坏 → UNAUTHENTICATED；签名失败/exp 过期 → SESSION_INVALID；sid 在拒绝名单 → SESSION_REVOKED。
func (s *Server) checkSession(r *http.Request) (sid, code string) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return "", "UNAUTHENTICATED"
	}
	claims, err := s.sessions.Verify(c.Value)
	if err != nil {
		if err == crypto.ErrSessionFormat {
			return "", "UNAUTHENTICATED"
		}
		return "", "SESSION_INVALID"
	}
	if s.sessions.Revoked(claims.SID) {
		return "", "SESSION_REVOKED"
	}
	return claims.SID, ""
}

// isAuthenticated 当前请求是否持有效会话（admin 模式下才有意义；none 模式恒 false → 掩码永不展示）。
func (s *Server) isAuthenticated(r *http.Request) bool {
	if !s.cfg.Load().IsAdmin() {
		return false
	}
	_, code := s.checkSession(r)
	return code == ""
}

// ---------- 通用响应 ----------

type errBody struct {
	Error errInner `json:"error"`
}

type errInner struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

type validationBody struct {
	Error   errInner                 `json:"error"`
	Details []config.ValidationError `json:"details"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errBody{Error: errInner{Code: code, Message: msg}})
}

func writeAuthErr(w http.ResponseWriter, code string) {
	var msg string
	switch code {
	case "UNAUTHENTICATED":
		msg = "请先登录"
	case "SESSION_INVALID":
		msg = "会话无效或已过期，请重新登录"
	case "SESSION_REVOKED":
		msg = "会话已登出，请重新登录"
	default:
		msg = "未认证"
	}
	writeErr(w, http.StatusUnauthorized, code, msg)
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// maxDrainBytes 读干 body 的上限（超过部分由 net/http 在关连接时丢弃）。
const maxDrainBytes = 64 << 10

// drainBody 读干请求体（有界）。
// 必要性：handler 未消费 body 就写响应时，net/http 会关闭连接（响应带 close 语义），
// 若客户端仍在发 body 就可能收到 RST、**丢掉这个响应**——实测 Windows 本机「带 body 的
// PUT 未登录 → 401」3% 概率丢失（300 次 9 次），浏览器里表现为「保存失败」而非「请先登录」。
// 故所有「不读 body 就拒绝」的路径（鉴权 401、登录的 400/429）先读干 body。
func drainBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxDrainBytes))
}

// ---------- handlers ----------

// handleIndex 内嵌静态页。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.static)
}

// iconContentTypes 图标扩展名 → Content-Type。
var iconContentTypes = map[string]string{
	".ico": "image/x-icon",
	".svg": "image/svg+xml",
	".png": "image/png",
}

// handleFavicon GET /favicon.ico：浏览器默认探测路径。
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	s.serveIconBytes(w, "favicon.ico")
}

// handleIcon GET /icons/{file}：白名单内的内嵌图标。
func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	s.serveIconBytes(w, r.PathValue("file"))
}

func (s *Server) serveIconBytes(w http.ResponseWriter, name string) {
	b, ok := s.icons[name]
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "未找到该资源")
		return
	}
	ct := "application/octet-stream"
	if t, ok := iconContentTypes[strings.ToLower(name[strings.LastIndexByte(name, '.'):])]; ok {
		ct = t
	}
	// 图标随二进制发布、内容不可变：可长缓存（静态页本身 no-cache，与此互不影响）
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// handleQuotas GET /api/quotas：快照序列化（公开）。
func (s *Server) handleQuotas(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Get()
	if snap == nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "快照不可用")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleGetConfig GET /api/config：脱敏视图（掩码仅登录可见）。
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	f := s.file.Load()
	authed := s.isAuthenticated(r)
	writeJSON(w, http.StatusOK, config.BuildView(f, *s.masks.Load(), authed, s.pwDef.Load()))
}

// handleLogin POST /api/login：JSON-only（CSRF 防线③）→ 全局限速 → bcrypt 比对 → 签发会话。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		drainBody(r)
		writeErr(w, http.StatusBadRequest, "INVALID_CONTENT_TYPE", "Content-Type 必须为 application/json")
		return
	}
	if ok, wait := s.limiter.Allow(); !ok {
		drainBody(r)
		rs := int(wait/time.Second) + 1
		writeJSON(w, http.StatusTooManyRequests, errBody{Error: errInner{
			Code: "RATE_LIMITED", Message: "失败次数过多，登录已锁定，请稍后再试", RetryAfterS: &rs,
		}})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	body, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体非法")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体须为 JSON 对象")
		return
	}
	hash := s.file.Load().Auth.PasswordHash
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		s.limiter.Fail()
		logx.Warnf("登录失败（%s）", clientIP(r))
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "密码错误")
		return
	}
	s.limiter.Success()
	val, err := s.sessions.Issue()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "会话签发失败")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    val,
		Path:     "/",
		MaxAge:   int(crypto.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// 不设 Secure：纯 HTTP 部署下浏览器拒收（PRD r1 取舍）
	})
	logx.Infof("登录成功（%s）", clientIP(r))
	writeOK(w)
}

// handleLogout POST /api/logout：sid 入拒绝名单 + 清 cookie。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		if claims, verr := s.sessions.Verify(c.Value); verr == nil {
			s.sessions.Revoke(claims.SID, claims.Exp)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/", MaxAge: 0,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeOK(w)
}

// handlePutConfig PUT /api/config：唯一写入口。全量替换 + 空 token 保留原值 + 原子写盘成功后热生效。
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体过大或读取失败")
		return
	}
	put, perr := config.ParsePut(body)
	if perr != nil {
		writeJSON(w, http.StatusBadRequest, validationBody{
			Error:   errInner{Code: "VALIDATION_ERROR", Message: perr.Error()},
			Details: []config.ValidationError{{Field: "body", Message: perr.Error()}},
		})
		return
	}
	if details := config.Validate(put); len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, validationBody{
			Error:   errInner{Code: "VALIDATION_ERROR", Message: "配置校验失败"},
			Details: details,
		})
		return
	}

	old := s.file.Load()
	newFile, masks, merr := s.mergeForSave(old, put)
	if merr != nil {
		writeErr(w, http.StatusInternalServerError, "SAVE_FAILED", merr.Error())
		return
	}
	if err := persist.SaveConfig(s.configPath, newFile); err != nil {
		// 磁盘为准：写盘失败内存不变（所见即所得）
		logx.Errorf("配置写盘失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "SAVE_FAILED", "配置写盘失败："+err.Error())
		return
	}

	// 内存 swap → 热生效
	runtime := config.BuildRuntime(newFile, s.key)
	s.file.Store(newFile)
	s.cfg.Store(runtime)
	s.masks.Store(&masks)
	s.pwDef.Store(config.IsDefaultPassword(newFile.Auth.PasswordHash))
	if s.onChanged != nil {
		s.onChanged(runtime)
	}
	keys := 0
	for i := range newFile.Providers {
		keys += newFile.Providers[i].KeyCount()
	}
	logx.Infof("配置已保存并热生效（%d 个平台 / %d 个 API Key，listen %s:%d）",
		len(newFile.Providers), keys, newFile.Listen.Host, newFile.Listen.Port)
	writeOK(w)
}

// mergeForSave 把 PUT 请求体合并为新的落盘形态：
// token 空 = 保留原 cipher（改凭据名/停用不影响 token）；非空 = 新明文加密替换 + 重算掩码 hint。
// 凭据身份 = <platform>.<keyID>：keyID 变了视为新凭据（不继承 token），与旧版「改 id 即新平台」语义一致。
// auth.password 空 = 保留原 hash；非空 = bcrypt 新 hash。
func (s *Server) mergeForSave(old *config.File, put *config.Put) (*config.File, map[string]string, error) {
	oldKeys := map[string]config.AccessKey{}
	for i := range old.Providers {
		fp := &old.Providers[i]
		for j := range fp.AccessKeys {
			k := fp.AccessKeys[j]
			oldKeys[config.RuntimeID(fp.Platform, k.ID)] = k
		}
	}
	newFile := &config.File{
		Version:   config.CurrentVersion,
		Listen:    put.Listen,
		Collector: put.Collector,
		Auth:      config.FileAuth{Mode: put.Auth.Mode, PasswordHash: old.Auth.PasswordHash},
		Providers: make([]config.FileProvider, 0, len(put.Providers)),
	}
	if put.Auth.Password != "" {
		hash, err := config.HashPassword(put.Auth.Password)
		if err != nil {
			return nil, nil, err
		}
		newFile.Auth.PasswordHash = hash
	}
	masks := map[string]string{}
	for i := range put.Providers {
		pp := &put.Providers[i]
		fp := config.FileProvider{
			Platform:   pp.Platform,
			AccessKeys: make([]config.AccessKey, 0, len(pp.AccessKeys)),
		}
		for j := range pp.AccessKeys {
			pk := &pp.AccessKeys[j]
			id := config.RuntimeID(pp.Platform, pk.ID)
			ak := config.AccessKey{
				ID:      pk.ID,
				Name:    pk.Name,
				Enabled: config.CopyBoolPtr(pk.Enabled),
			}
			switch {
			case pk.Token != "":
				cipher, err := crypto.SealToken(s.key, pk.Token)
				if err != nil {
					return nil, nil, err
				}
				ak.TokenCipher = cipher
				masks[id] = crypto.Mask(pk.Token)
			default:
				if o, ok := oldKeys[id]; ok {
					ak.TokenCipher = o.TokenCipher
					if m := (*s.masks.Load())[id]; m != "" {
						masks[id] = m
					}
				}
			}
			fp.AccessKeys = append(fp.AccessKeys, ak)
		}
		newFile.Providers = append(newFile.Providers, fp)
	}
	// flag 覆盖保持（-interval/-addr/-port > config，即使保存后本进程内仍覆盖）
	s.applyOverrides(&newFile.Listen, &newFile.Collector)
	return newFile, masks, nil
}

func (s *Server) applyOverrides(l *config.Listen, c *config.Collector) {
	if s.overrides.IntervalBaseS > 0 {
		c.IntervalBaseS = s.overrides.IntervalBaseS
	}
	if s.overrides.Host != "" {
		l.Host = s.overrides.Host
	}
	if s.overrides.Port > 0 {
		l.Port = s.overrides.Port
	}
}

// handleTest POST /api/test：以 paths[0] 测连通性；临时 token 仅本次请求内存使用，不落盘不进日志。
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体过大或读取失败")
		return
	}
	var req struct {
		ProviderID string `json:"provider_id"`
		Token      string `json:"token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体须为 JSON 对象")
		return
	}
	cfg := s.cfg.Load()
	p := cfg.Provider(req.ProviderID)
	if p == nil {
		writeErr(w, http.StatusBadRequest, "PROVIDER_NOT_FOUND", "未找到该平台凭据："+req.ProviderID)
		return
	}
	if len(p.Paths) == 0 {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "该平台没有可用 path")
		return
	}
	probe := *p // 值拷贝：临时 token 仅本次请求内存使用
	if req.Token != "" {
		probe.Token = req.Token
		probe.DecryptFailed = false
		logx.RegisterSecret(req.Token) // 脱敏红线兜底
	}
	if probe.Token == "" {
		writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", "无可用 token（该平台未配置，请求也未携带临时 token）")
		return
	}

	t0 := time.Now()
	res := s.client.FetchOnce(r.Context(), &probe, probe.Paths[0])
	latency := time.Since(t0).Milliseconds()
	if res.Class != collector.ClassOK {
		msg := res.Message
		if res.Summary != "" && res.Summary != res.Message {
			msg = res.Summary
		}
		if res.Status > 0 && !strings.HasPrefix(msg, "HTTP ") {
			msg = "HTTP " + strconv.Itoa(res.Status) + " " + msg
		}
		// 不透传上游响应头，token 及其派生值永不回显
		writeErr(w, http.StatusBadGateway, "UPSTREAM_ERROR", msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "latency_ms": latency})
}

// clientIP 返回请求来源 IP（仅日志展示用）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// readBody 读取请求体并限长。
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBodyBytes {
		return nil, errTooLarge
	}
	return data, nil
}

var errTooLarge = errStr("body too large")

type errStr string

func (e errStr) Error() string { return string(e) }
