// Package config 承载 QuotaClock 配置的三形态与转换：
//
//	File  落盘形态（token_cipher / password_hash）
//	View  GET /api/config 脱敏响应（has_token / token_masked[仅登录] / password_is_default；无任何 token 字段）
//	Put   PUT /api/config 请求体（token / password 为仅输入明文；计算字段忽略；敏感字段拒绝）
//
// 契约见 PRD 附录 A 与详细设计 §5。
package config

import "strconv"

// CurrentVersion 是当前配置 schema 版本（version 2 = v0.1，version 3 = v0.2）。
const CurrentVersion = 3

// AuthMode 常量。
const (
	AuthModeAdmin = "admin"
	AuthModeNone  = "none"
)

// AuthStyle 常量（S1 增量）。
const (
	AuthStyleBearer = "bearer"
	AuthStyleCookie = "cookie"
)

// Listen 监听配置。
type Listen struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Addr 返回 http.Server 使用的 host:port。
func (l Listen) Addr() string { return l.Host + ":" + strconv.Itoa(l.Port) }

// Collector 采集调度参数。
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

// ---------- 落盘形态 ----------

// FileAuth 落盘 auth 段：仅 bcrypt hash，永不落明文。
type FileAuth struct {
	Mode         string `json:"mode"`
	PasswordHash string `json:"password_hash"`
}

// FileProvider 落盘 provider：token 只存密文。
type FileProvider struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	Paths        []string          `json:"paths"`
	AuthStyle    string            `json:"auth_style,omitempty"`    // 缺省 bearer
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"` // S2 增量
	TokenCipher  string            `json:"token_cipher,omitempty"`
}

// File 配置文件落盘形态。
type File struct {
	Version   int            `json:"version"`
	Listen    Listen         `json:"listen"`
	Collector Collector      `json:"collector"`
	Auth      FileAuth       `json:"auth"`
	Providers []FileProvider `json:"providers"`
}

// ---------- GET /api/config 脱敏视图 ----------

// ViewAuth 脱敏 auth 段。
// Authenticated 为契约增量（实现期声明）：前端渲染登录/登出按钮与掩码显隐所需。
type ViewAuth struct {
	Mode              string `json:"mode"`
	PasswordIsDefault bool   `json:"password_is_default"`
	Authenticated     bool   `json:"authenticated"`
}

// ViewProvider 脱敏 provider：无 token/token_cipher 字段；TokenMasked 仅登录输出。
type ViewProvider struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	Paths        []string          `json:"paths"`
	AuthStyle    string            `json:"auth_style,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
	HasToken     bool              `json:"has_token"`
	TokenMasked  string            `json:"token_masked,omitempty"`
}

// View GET /api/config 响应全集（PUT 全量替换的回传素材）。
type View struct {
	Version   int            `json:"version"`
	Listen    Listen         `json:"listen"`
	Collector Collector      `json:"collector"`
	Auth      ViewAuth       `json:"auth"`
	Providers []ViewProvider `json:"providers"`
}

// ---------- PUT /api/config 请求体 ----------

// PutProvider 请求中的 provider：Token 为仅输入明文（空 = 保留原值）。
type PutProvider struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	Paths        []string          `json:"paths"`
	AuthStyle    string            `json:"auth_style,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
	Token        string            `json:"token,omitempty"`
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

// RuntimeProvider 运行期 provider：Token 仅存在于内存。
type RuntimeProvider struct {
	ID           string
	Name         string
	BaseURL      string
	Paths        []string
	AuthStyle    string
	ExtraHeaders map[string]string
	TokenCipher  string
	Token        string // 解密明文；DecryptFailed 时为空
	TokenMasked  string // 内存 hint（设计 D-5）
	// DecryptFailed 表示 token_cipher 解密失败（key.bin 丢失/不匹配）：
	// 采集器应将其标记 token_invalid（错误码 TOKEN_DECRYPT_FAILED），不 crash（设计 §6.4）。
	DecryptFailed bool
}

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
