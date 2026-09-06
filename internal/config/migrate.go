package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
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

// ---------- v0.1 (version 2) 迁移：方案 A 自动改写 + 兜底（设计 §5.5） ----------

// v0.1 代理转发表（llm-proxy.py ROUTES 的冻结快照，仅作迁移模板用）。
const (
	v1ProxyPrefix = "http://127.0.0.1:8787"
	kimiUpstream  = "https://api.kimi.com/coding/v1"
	openUpstream  = "https://opencode.ai"
)

var serverIDRe = regexp.MustCompile(`[?&]id=([a-f0-9]+)`)

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

// MigrateV1 把 v0.1 导出 JSON（version 2）转为 v0.2 File（version 3）：
// token 加密落盘、代理平台自动改写、enabled=false 跳过。返回结果与迁移日志行（WARN/INFO）。
// 迁移完成后明文即焚：仅存在于 seal 闭包与返回值之外的本函数栈内，不落日志。
func MigrateV1(raw []byte, seal SealFunc) (*File, []string, error) {
	var v1 v1Config
	if err := json.Unmarshal(raw, &v1); err != nil {
		return nil, nil, fmt.Errorf("v0.1 配置解析失败: %w", err)
	}
	logs := []string{}
	warnf := func(format string, args ...any) {
		logs = append(logs, "WARN "+fmt.Sprintf(format, args...))
	}
	infof := func(format string, args ...any) {
		logs = append(logs, "INFO "+fmt.Sprintf(format, args...))
	}

	defHash, err := HashPassword(DefaultPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("生成默认密码 hash 失败: %w", err)
	}
	out := &File{
		Version:   CurrentVersion,
		Listen:    DefaultListen(),
		Collector: DefaultCollector(),
		Auth:      FileAuth{Mode: AuthModeAdmin, PasswordHash: defHash},
		Providers: []FileProvider{},
	}

	for i := range v1.Providers {
		vp := &v1.Providers[i]
		if vp.Enabled != nil && !*vp.Enabled {
			warnf("迁移：provider %q enabled=false，未导入（v0.2 无 enabled 概念，如需启用请在设置中手动添加）", vp.ID)
			continue
		}
		fp, rewritten, skipWarn := migrateProvider(vp, seal, infof, warnf)
		if skipWarn != "" {
			warnf("%s", skipWarn)
			continue
		}
		out.Providers = append(out.Providers, *fp)
		_ = rewritten
	}
	return out, logs, nil
}

// migrateProvider 迁移单个 provider；第三个返回值非空 = 兜底跳过原因（该 provider 不导入）。
func migrateProvider(vp *v1Provider, seal SealFunc, infof, warnf func(string, ...any)) (*FileProvider, bool, string) {
	fp := &FileProvider{
		ID:      vp.ID,
		Name:    vp.Name,
		BaseURL: vp.BaseURL,
		Paths:   []string{},
	}
	// endpoints → paths（保持顺序，仅 GET；params 非空且非 "{" 时 WARN）
	remainder := ""
	for _, ep := range vp.Endpoints {
		if ep.Method != "GET" {
			warnf("迁移：provider %q 的 %s %s 非 GET，未导入", vp.ID, ep.Method, ep.Path)
			continue
		}
		if p := strings.TrimSpace(ep.Params); p != "" && p != "{" {
			warnf("迁移：provider %q 的 %s 带参数 %q，v0.2 不迁移 buildQuery 语义，请人工确认并改写为 URL query", vp.ID, ep.Path, p)
		}
		fp.Paths = append(fp.Paths, ep.Path)
	}
	if len(fp.Paths) == 0 {
		// 无可用 path：构造校验会拒绝，直接兜底跳过
		return nil, false, fmt.Sprintf("迁移：provider %q 没有可用的 GET path，已跳过（请在设置中手动添加 base_url 与 paths）", vp.ID)
	}

	// 方案 A：代理平台自动改写
	base := strings.TrimRight(vp.BaseURL, "/")
	switch {
	case base == v1ProxyPrefix+"/kimi" || strings.HasPrefix(base, v1ProxyPrefix+"/kimi/"):
		remainder = base[len(v1ProxyPrefix+"/kimi"):]
		fp.BaseURL = kimiUpstream
		if remainder != "" && remainder != "/" {
			fp.Paths[0] = remainder + fp.Paths[0]
		}
		infof("迁移改写：%s → base_url=%s（paths[0] 并入前缀余量 %q）", vp.BaseURL, fp.BaseURL, remainder)
	case base == v1ProxyPrefix+"/opencode" || strings.HasPrefix(base, v1ProxyPrefix+"/opencode/"):
		remainder = base[len(v1ProxyPrefix+"/opencode"):]
		fp.BaseURL = openUpstream
		if remainder != "" && remainder != "/" {
			fp.Paths[0] = remainder + fp.Paths[0]
		}
		fp.AuthStyle = AuthStyleCookie
		for _, p := range fp.Paths {
			if m := serverIDRe.FindStringSubmatch(p); m != nil {
				if fp.ExtraHeaders == nil {
					fp.ExtraHeaders = map[string]string{}
				}
				fp.ExtraHeaders["x-server-id"] = m[1]
				fp.ExtraHeaders["x-server-instance"] = "server-fn:5"
				break
			}
		}
		if _, ok := fp.ExtraHeaders["x-server-id"]; !ok {
			warnf("迁移：provider %q 的 path 中未找到 id= 参数，extra_headers.x-server-id 需人工补充", vp.ID)
		}
		infof("迁移改写：%s → base_url=%s auth_style=cookie extra_headers={x-server-id, x-server-instance: server-fn:5}（原地址保留 query）", vp.BaseURL, fp.BaseURL)
	default:
		if u, err := url.Parse(vp.BaseURL); err == nil && isLoopbackHost(u.Hostname()) {
			// 指向本机代理但不匹配冻结转发表：不猜、不改写（设计 §5.5 兜底）
			return nil, false, fmt.Sprintf(
				"迁移：provider %q 的 base_url %q 指向本机代理但不是已知的 /kimi 或 /opencode 转发表（可能自定义过端口），已跳过。"+
					"请参照 README 迁移对照表手动添加：真实上游 base_url、paths、auth_style（opencode 还需 extra_headers）", vp.ID, vp.BaseURL)
		}
		infof("迁移：provider %q 直连 %s，原样导入", vp.ID, vp.BaseURL)
	}

	// token 明文 → AES-GCM 密文（迁移完成明文即焚）
	if vp.Token != "" {
		cipher, err := seal(vp.Token)
		if err != nil {
			// 密封失败则整体失败（调用方退出），避免静默丢失 token
			return nil, false, fmt.Sprintf("迁移：provider %q token 加密失败：%v", vp.ID, err)
		}
		fp.TokenCipher = cipher
	}
	return fp, true, ""
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
