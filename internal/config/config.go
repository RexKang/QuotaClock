// Package config 承载 QuotaClock 配置的三形态与转换：
//
//	File  落盘形态（token_cipher / password_hash；v0.2.5 起 provider 只存平台标识与凭据）
//	View  GET /api/config 脱敏响应（has_token / token_masked[仅登录] / password_is_default；无任何 token 字段）
//	Put   PUT /api/config 请求体（token / password 为仅输入明文；计算字段忽略；敏感字段拒绝）
//
// 契约见 PRD 附录 A 与详细设计 §5。
package config

import "strconv"

// CurrentVersion 是当前配置 schema 版本
// （2 = v0.1，3 = v0.2.0~v0.2.4，4 = v0.2.5：平台预设 + 同平台多 key）。
const CurrentVersion = 4

// AuthMode 常量。
const (
	AuthModeAdmin = "admin"
	AuthModeNone  = "none"
)

// AuthStyle 常量（S1 增量）。
// v0.2.5：四个内置平台的鉴权方式统一为 Bearer（Authorization 头）——历史上支持过的
// cookie 方式随「没有 cookie 平台」一并移除（客户端只实现 Bearer，见 collector/client.go）。
const AuthStyleBearer = "bearer"

// Listen 监听配置。
type Listen struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Addr 返回 http.Server 使用的 host:port。
func (l Listen) Addr() string { return l.Host + ":" + strconv.Itoa(l.Port) }

// Collector 采集调度参数。
//
// v0.2.5 语义澄清：IntervalBaseS 是**每个 key** 的采集周期（本次返回后到下次发起）。
// 同一平台下的多个 key 在该周期内等分错开（key i 的偏移 = i × IntervalBaseS / keyCount），
// 使同平台的请求彼此拉开、不形成突发（用户实测：同平台不同 key 同时打会互相冲突）。
type Collector struct {
	IntervalBaseS     int `json:"interval_base_s"`
	JitterMinS        int `json:"jitter_min_s"`
	JitterMaxS        int `json:"jitter_max_s"`
	StaggerMinS       int `json:"stagger_min_s"`
	StaggerMaxS       int `json:"stagger_max_s"`
	BackoffMultiplier int `json:"backoff_multiplier"`
	BackoffMaxS       int `json:"backoff_max_s"`
}

// DefaultCollector 返回 PRD 默认值（300s + 5~25s 抖动，错峰 1~5s，×2 封顶 1800s）。
func DefaultCollector() Collector {
	return Collector{
		IntervalBaseS:     300,
		JitterMinS:        5,
		JitterMaxS:        25,
		StaggerMinS:       1,
		StaggerMaxS:       5,
		BackoffMultiplier: 2,
		BackoffMaxS:       1800,
	}
}

// DefaultListen 返回 PRD 默认监听（127.0.0.1:8787）。
func DefaultListen() Listen { return Listen{Host: "127.0.0.1", Port: 8787} }

// BoolPtr 返回 v 的地址（构造 *bool 字段用）。
func BoolPtr(v bool) *bool { return &v }

// CopyBoolPtr 复制 *bool（nil 透传）：避免落盘视图与请求体/运行时共享同一指针。
func CopyBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// RuntimeID 拼装运行时 provider 标识：<platform>.<keyID>。
// 快照、缓存、退避状态、前端卡片都以它为最小单元（一个 key 一张卡）。
func RuntimeID(platform, keyID string) string { return platform + "." + keyID }

// ---------- 落盘形态 ----------

// FileAuth 落盘 auth 段：仅 bcrypt hash，永不落明文。
type FileAuth struct {
	Mode         string `json:"mode"`
	PasswordHash string `json:"password_hash"`
}

// AccessKey 平台下的单个访问凭据（v0.2.5）：token 只存密文。
type AccessKey struct {
	ID          string `json:"id"`                     // 平台内唯一（同时作为运行时 provider id 的后半段）
	Name        string `json:"name"`                   // 展示名（如 "A1"/"主号"），空则回落平台名
	TokenCipher string `json:"token_cipher,omitempty"` // 密文；空 = 未配置
	Enabled     *bool  `json:"enabled,omitempty"`      // nil = 启用
}

// IsEnabled 缺省（nil）= 启用；值接收者，便于切片取元素直接判定。
func (k AccessKey) IsEnabled() bool { return k.Enabled == nil || *k.Enabled }

// FileProvider 落盘 provider（v0.2.5）：一个平台一条，下挂 N 个访问凭据。
// base_url / paths / auth_style 由 Platform 预设派生，不落盘——杜绝「配置里的地址」与
// 「代码里的解析逻辑」错配。
type FileProvider struct {
	Platform   string      `json:"platform"`
	AccessKeys []AccessKey `json:"access_keys"`
}

// File 配置文件落盘形态。
type File struct {
	Version   int            `json:"version"`
	Listen    Listen         `json:"listen"`
	Collector Collector      `json:"collector"`
	Auth      FileAuth       `json:"auth"`
	Providers []FileProvider `json:"providers"`
}

// KeyCount 该平台下凭据总数。
func (p *FileProvider) KeyCount() int { return len(p.AccessKeys) }

// ---------- GET /api/config 脱敏视图 ----------

// ViewAuth 脱敏 auth 段。
// Authenticated 为契约增量（实现期声明）：前端渲染登录/登出按钮与掩码显隐所需。
type ViewAuth struct {
	Mode              string `json:"mode"`
	PasswordIsDefault bool   `json:"password_is_default"`
	Authenticated     bool   `json:"authenticated"`
}

// ViewAccessKey 脱敏凭据：无 token/token_cipher 字段。
type ViewAccessKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	HasToken    bool   `json:"has_token"`
	TokenMasked string `json:"token_masked,omitempty"` // 仅登录可见
	Enabled     bool   `json:"enabled"`                // 无 omitempty：false 也要输出
}

// ViewProvider 脱敏 provider：平台预设信息直接带出（前端无需自维护映射表）。
type ViewProvider struct {
	Platform     string          `json:"platform"`
	PlatformName string          `json:"platform_name"`
	BaseURL      string          `json:"base_url"` // 只读展示用（不可配置）
	Paths        []string        `json:"paths"`    // 只读展示用（不可配置）
	AccessKeys   []ViewAccessKey `json:"access_keys"`
}

// View GET /api/config 响应全集（PUT 全量替换的回传素材）。
type View struct {
	Version   int            `json:"version"`
	Listen    Listen         `json:"listen"`
	Collector Collector      `json:"collector"`
	Auth      ViewAuth       `json:"auth"`
	Providers []ViewProvider `json:"providers"`
	Platforms []ViewPlatform `json:"platforms"` // 内置平台目录（前端下拉用，非敏感）
}

// ViewPlatform 内置平台目录项：前端不必自行维护平台清单（单一真源在 Go 的 platforms 表）。
type ViewPlatform struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	Paths   []string `json:"paths"`
}

// ---------- PUT /api/config 请求体 ----------

// PutAccessKey 请求中的凭据：Token 为仅输入明文（空 = 保留原值）。
type PutAccessKey struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Token   string `json:"token,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// IsEnabled 请求体语义同落盘：nil = 启用（兼容旧客户端不传该字段）。
func (k PutAccessKey) IsEnabled() bool { return k.Enabled == nil || *k.Enabled }

// PutProvider 请求中的 provider：只允许平台标识 + 凭据。
// base_url/paths/auth_style 等派生字段在 wire 层被容忍并剥离（GET 响应回传时自然携带），
// 平台预设才是唯一真源——手改这些字段不会生效，也不会报错。
type PutProvider struct {
	Platform   string         `json:"platform"`
	AccessKeys []PutAccessKey `json:"access_keys"`
}

// PutAuth 请求中的 auth 段：Password 为仅输入明文（空 = 保留原 hash）。
type PutAuth struct {
	Mode     string `json:"mode"`
	Password string `json:"password,omitempty"`
}

// Put PUT /api/config 请求体（全量替换）。
type Put struct {
	Version   int           `json:"version"`
	Listen    Listen        `json:"listen"`
	Collector Collector     `json:"collector"`
	Auth      PutAuth       `json:"auth"`
	Providers []PutProvider `json:"providers"`
}

// ---------- 运行时视图（内存，含解密 token） ----------

// RuntimeProvider 运行期 provider：一个 AccessKey 展开成一条（扁平），Token 仅在内存。
// Enabled 用 *bool：nil = 启用（与落盘层同语义）。零值结构体因此默认「启用」，
// 杜绝「构造时忘记赋值 → 静默停采」这类零值陷阱（v0.2.4 回归项）。
type RuntimeProvider struct {
	ID           string // = RuntimeID(Platform, KeyID)
	Platform     string // 调度分组用（同平台等分错峰）
	KeyID        string
	KeyName      string // 用户填的凭据名（如 "A1"），空则回落平台名
	Name         string // 展示名："平台名 · key名"
	BaseURL      string // 由平台预设派生
	Paths        []string
	AuthStyle    string
	ExtraHeaders map[string]string
	TokenCipher  string
	Enabled      *bool
	Token        string // 解密明文；DecryptFailed 时为空
	TokenMasked  string // 内存 hint（设计 D-5）
	// DecryptFailed 表示 token_cipher 解密失败（key.bin 丢失/不匹配）：
	// 采集器应将其标记 token_invalid（错误码 TOKEN_DECRYPT_FAILED），不 crash（设计 §6.4）。
	DecryptFailed bool
	// KeyIndex/KeyCount：同平台内的序号与总数（调度等分偏移 = KeyIndex × base/KeyCount）。
	KeyIndex int
	KeyCount int
}

// IsEnabled 返回该 provider 是否参与采集（nil 缺省视为启用）。
func (p *RuntimeProvider) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

// Runtime 配置的运行时视图，热生效即整体换指针。
type Runtime struct {
	Version      int
	Listen       Listen
	Collector    Collector
	AuthMode     string
	PasswordHash []byte
	Providers    []*RuntimeProvider
}

// Provider 按 id 查找（未命中返回 nil）。
func (r *Runtime) Provider(id string) *RuntimeProvider {
	for _, p := range r.Providers {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// IsAdmin 是否 admin 模式。
func (r *Runtime) IsAdmin() bool { return r.AuthMode == AuthModeAdmin }
