package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// DefaultPassword 首次默认密码（PRD §6.2；源码公开后风险已知，页面强提示修改）。
const DefaultPassword = "Quota@2026090S"

// BcryptCost 统一 bcrypt 代价。
const BcryptCost = 10

// HashPassword 生成 bcrypt hash。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), BcryptCost)
	return string(b), err
}

// IsDefaultPassword 比对现存 hash 是否等于默认密码（默认密码未修改检测，PRD §6.2）。
func IsDefaultPassword(hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(DefaultPassword)) == nil
}

// ---------- 迁移通用：旧形态「一 key 一 provider」→ v0.2.5「一平台多凭据」 ----------

// legacyProvider 承载 v0.1（version 2）与 v0.2.x（version 3）的 provider 字段并集
// （两种旧形态都允许用户自填 base_url/paths/auth_style）。
type legacyProvider struct {
	ID           string
	Name         string
	BaseURL      string
	Paths        []string
	AuthStyle    string
	ExtraHeaders map[string]string
	TokenCipher  string
	Enabled      *bool
}

// groupLegacyProviders 把旧形态归并为 v0.2.5 结构：
//   - 平台由 base_url 推断（InferPlatform）；推断不出的条目跳过并记入 logs（不猜地址）
//   - 同一平台的多条旧 provider 合并为一条 FileProvider，各自成为一个 access_key
//   - key id 取旧 provider id（旧 id 全局唯一，平台内必然唯一；防御性去重）
//   - key name 从旧 name 派生（与平台名相同的去掉，含平台名前缀的剥前缀）
//
// 返回 (providers, logs)：logs 为已带级别前缀的迁移日志行（"WARN …"/"INFO …"）。
func groupLegacyProviders(items []legacyProvider) ([]FileProvider, []string) {
	logs := []string{}
	warnf := func(format string, args ...any) { logs = append(logs, "WARN "+fmt.Sprintf(format, args...)) }
	infof := func(format string, args ...any) { logs = append(logs, "INFO "+fmt.Sprintf(format, args...)) }

	order := []string{} // 保持配置顺序
	byPlatform := map[string]*FileProvider{}
	for i := range items {
		it := &items[i]
		pid, ok := InferPlatform(it.BaseURL)
		if !ok {
			warnf("迁移：平台条目 %q 的地址 %s 不在已知平台清单，已跳过；请在设置里从「智谱 GLM / DeepSeek / Kimi Code / OpenCode」中重新添加（详见 README「从 v0.1 升级」）",
				it.ID, it.BaseURL)
			continue
		}
		preset, _ := PlatformByID(pid)
		fp := byPlatform[pid]
		if fp == nil {
			fp = &FileProvider{Platform: pid, AccessKeys: []AccessKey{}}
			byPlatform[pid] = fp
			order = append(order, pid)
		}
		keyID := it.ID
		if keyID == "" {
			keyID = fmt.Sprintf("k%d", len(fp.AccessKeys)+1)
		}
		for _, existing := range fp.AccessKeys { // 防御性去重
			if existing.ID == keyID {
				keyID = keyID + "-2"
			}
		}
		fp.AccessKeys = append(fp.AccessKeys, AccessKey{
			ID:          keyID,
			Name:        deriveKeyName(it.Name, preset.Name),
			TokenCipher: it.TokenCipher,
			Enabled:     CopyBoolPtr(it.Enabled),
		})
		// 旧配置里的 paths 只做对照提示（新版本的 paths 由预设固定）
		if len(it.Paths) > 0 && !samePaths(it.Paths, preset.Paths) {
			infof("迁移：%s 的请求路径按平台预设收敛为 %s（原 %s）",
				preset.Name, strings.Join(preset.Paths, ","), strings.Join(it.Paths, ","))
		}
	}
	out := make([]FileProvider, 0, len(order))
	for _, pid := range order {
		fp := *byPlatform[pid]
		if len(fp.AccessKeys) > 1 {
			infof("迁移：平台 %s 归并了 %d 个凭据为一个平台条目（调度上按等分周期错开采集）",
				pid, len(fp.AccessKeys))
		}
		out = append(out, fp)
	}
	return out, logs
}

// deriveKeyName 从旧 provider 名派生凭据名：与平台名相同 → 空（展示回落平台名）；
// 带平台名前缀 → 剥掉前缀；其余原样保留。
func deriveKeyName(oldName, platformName string) string {
	n := strings.TrimSpace(oldName)
	if n == "" || platformName == "" {
		return n
	}
	if strings.EqualFold(n, platformName) {
		return ""
	}
	if len(n) > len(platformName) && strings.EqualFold(n[:len(platformName)], platformName) {
		rest := strings.TrimLeft(n[len(platformName):], " -·:_")
		if rest != "" {
			return rest
		}
	}
	return n
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------- v0.1（version 2）迁移：方案 A 自动改写 + 兜底（设计 §5.5） ----------

// v0.1 代理转发表（llm-proxy.py ROUTES 的冻结快照）。
const (
	v1ProxyPrefix = "http://127.0.0.1:8787"
	kimiUpstream  = "https://api.kimi.com/coding/v1"
	// 260907 起 opencode 提供官方用量接口（Bearer API Key），替代 Cookie+/_server 逆向。
	openUsageBase = "https://opencode.ai/zen/go/v1"
)

type v1Endpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Params string `json:"params"`
}

type v1Provider struct {
	Enabled   *bool        `json:"enabled"` // 缺省视为 true（复刻 v0.1 语义）
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	BaseURL   string       `json:"baseURL"`
	Token     string       `json:"token"`
	Endpoints []v1Endpoint `json:"endpoints"`
}

type v1Config struct {
	Version   int          `json:"version"`
	Providers []v1Provider `json:"providers"`
}

// SealFunc 把明文 token 加密为落盘密文（迁移时注入 crypto.SealToken）。
type SealFunc func(plaintext string) (string, error)

// MigrateV1 把 v0.1 导出 JSON（version 2）转为当前 File：
// token 加密落盘、代理平台自动改写、enabled=false 导入并保持停用，再归并为「一平台多凭据」。
// 返回结果与迁移日志行（WARN/INFO）。
// 迁移完成后明文即焚：仅存在于 seal 闭包与返回值之外的本函数栈内，不落日志。
func MigrateV1(raw []byte, seal SealFunc) (*File, []string, error) {
	var v1 v1Config
	if err := json.Unmarshal(raw, &v1); err != nil {
		return nil, nil, fmt.Errorf("v0.1 配置解析失败: %w", err)
	}
	logs := []string{}
	warnf := func(format string, args ...any) { logs = append(logs, "WARN "+fmt.Sprintf(format, args...)) }
	infof := func(format string, args ...any) { logs = append(logs, "INFO "+fmt.Sprintf(format, args...)) }

	defHash, err := HashPassword(DefaultPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("生成默认密码 hash 失败: %w", err)
	}

	var items []legacyProvider
	for i := range v1.Providers {
		vp := &v1.Providers[i]
		disabled := vp.Enabled != nil && !*vp.Enabled
		baseURL, paths, skipToken, skipWarn := rewriteV1Provider(vp, infof, warnf)
		if skipWarn != "" {
			warnf("%s", skipWarn)
			continue
		}
		it := legacyProvider{
			ID:      vp.ID,
			Name:    vp.Name,
			BaseURL: baseURL,
			Paths:   paths,
			Enabled: BoolPtr(true),
		}
		if disabled {
			// v0.2.4 起：v0.1 的停用平台照常导入并保持停用（不丢配置），用户可在设置里启用
			it.Enabled = BoolPtr(false)
			infof("迁移：%q 在 v0.1 中为停用状态，已导入并保持停用（如需启用请在设置中勾选）", vp.ID)
		}
		if vp.Token != "" && !skipToken {
			cipher, serr := seal(vp.Token)
			if serr != nil {
				// 密封失败则整体失败（调用方退出），避免静默丢失 token
				return nil, nil, fmt.Errorf("迁移：%q token 加密失败: %w", vp.ID, serr)
			}
			it.TokenCipher = cipher
		}
		items = append(items, it)
	}

	providers, glogs := groupLegacyProviders(items)
	logs = append(logs, glogs...)

	out := &File{
		Version:   CurrentVersion,
		Listen:    DefaultListen(),
		Collector: DefaultCollector(),
		Auth:      FileAuth{Mode: AuthModeAdmin, PasswordHash: defHash},
		Providers: providers,
	}
	return out, logs, nil
}

// rewriteV1Provider 处理 v0.1 → 直连的地址改写（方案 A）与 paths 提取。
// 返回 (baseURL, paths, skipToken, skipWarn)：skipWarn 非空 = 该条目跳过（原因文案）。
func rewriteV1Provider(vp *v1Provider, infof, warnf func(string, ...any)) (string, []string, bool, string) {
	paths := []string{}
	skipToken := false
	for _, ep := range vp.Endpoints {
		if ep.Method != "GET" {
			warnf("迁移：%q 的 %s %s 非 GET，未导入", vp.ID, ep.Method, ep.Path)
			continue
		}
		if p := strings.TrimSpace(ep.Params); p != "" && p != "{" {
			warnf("迁移：%q 的 %s 带参数 %q，v0.2 不迁移 buildQuery 语义，请人工确认并改写为 URL query", vp.ID, ep.Path, p)
		}
		paths = append(paths, ep.Path)
	}
	if len(paths) == 0 {
		return "", nil, false, fmt.Sprintf("迁移：%q 没有可用的 GET 路径，已跳过（请在设置中重新添加该平台）", vp.ID)
	}

	base := strings.TrimRight(vp.BaseURL, "/")
	switch {
	case base == v1ProxyPrefix+"/kimi" || strings.HasPrefix(base, v1ProxyPrefix+"/kimi/"):
		remainder := base[len(v1ProxyPrefix+"/kimi"):]
		if remainder != "" && remainder != "/" {
			paths[0] = remainder + paths[0]
		}
		infof("迁移改写：%s → base_url=%s（paths[0] 并入前缀余量 %q）", vp.BaseURL, kimiUpstream, remainder)
		return kimiUpstream, paths, false, ""
	case base == v1ProxyPrefix+"/opencode" || strings.HasPrefix(base, v1ProxyPrefix+"/opencode/"):
		// 官方用量接口：旧 Cookie+/_server 逆向不再适用；Cookie 登录态无法转 API Key，token 不迁移
		skipToken = true
		warnf("迁移：%q 已改写为官方用量接口 %s/usage（Bearer 鉴权）。v0.1 的 Cookie 登录态无法转换为 API Key，token 未迁移——请到控制台生成 API Key 后录入", vp.ID, openUsageBase)
		infof("迁移改写：%s → base_url=%s paths=[/usage] auth_style=bearer(缺省)", vp.BaseURL, openUsageBase)
		return openUsageBase, []string{"/usage"}, skipToken, ""
	default:
		if u, err := url.Parse(vp.BaseURL); err == nil && isLoopbackHost(u.Hostname()) {
			// 指向本机代理但不匹配冻结转发表：不猜、不改写（设计 §5.5 兜底）
			return "", nil, false, fmt.Sprintf(
				"迁移：%q 的 base_url %q 指向本机代理但不是已知的 /kimi 或 /opencode 转发表（可能自定义过端口），已跳过；请在设置里重新添加该平台（详见 README「从 v0.1 升级」）",
				vp.ID, vp.BaseURL)
		}
		infof("迁移：%q 直连 %s，按平台预设导入", vp.ID, vp.BaseURL)
		return vp.BaseURL, paths, false, ""
	}
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// ---------- v0.2.x（version 3）迁移：扁平 provider → 平台 + 多凭据 ----------

type v3Provider struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	Paths        []string          `json:"paths"`
	AuthStyle    string            `json:"auth_style"`
	ExtraHeaders map[string]string `json:"extra_headers"`
	TokenCipher  string            `json:"token_cipher"`
	Enabled      *bool             `json:"enabled"`
}

type v3File struct {
	Version   int          `json:"version"`
	Listen    Listen       `json:"listen"`
	Collector Collector    `json:"collector"`
	Auth      FileAuth     `json:"auth"`
	Providers []v3Provider `json:"providers"`
}

// MigrateV3 把 v0.2.0~v0.2.4 的配置（version 3，一 key 一 provider）升到 v0.2.5：
// 平台按 base_url 推断、同平台多条合并为一条 provider、旧 token 变成第一个凭据。
// listen / collector / auth / 密码 hash 原样保留（这几段 schema 未变）。
func MigrateV3(raw []byte) (*File, []string, error) {
	var v3 v3File
	if err := json.Unmarshal(raw, &v3); err != nil {
		return nil, nil, fmt.Errorf("配置解析失败: %w", err)
	}
	items := make([]legacyProvider, 0, len(v3.Providers))
	for i := range v3.Providers {
		p := &v3.Providers[i]
		items = append(items, legacyProvider{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Paths: p.Paths,
			AuthStyle: p.AuthStyle, ExtraHeaders: p.ExtraHeaders,
			TokenCipher: p.TokenCipher, Enabled: CopyBoolPtr(p.Enabled),
		})
	}
	providers, logs := groupLegacyProviders(items)
	out := &File{
		Version:   CurrentVersion,
		Listen:    v3.Listen,
		Collector: v3.Collector,
		Auth:      v3.Auth,
		Providers: providers,
	}
	if out.Listen.Host == "" || out.Listen.Port == 0 {
		out.Listen = DefaultListen()
	}
	if out.Collector.IntervalBaseS == 0 {
		out.Collector = DefaultCollector()
	}
	if out.Auth.Mode == "" {
		out.Auth.Mode = AuthModeAdmin
	}
	return out, logs, nil
}
