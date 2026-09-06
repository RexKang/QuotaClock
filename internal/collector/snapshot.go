package collector

import (
	"bytes"
	"encoding/json"
	"sync/atomic"
	"time"
)

// ErrorInfo 快照 error 对象：message 为稳定文案、retry_after_s 数值化（倒计时前端算）。
type ErrorInfo struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

// 常用错误码。
const (
	CodeNotCollectedYet  = "NOT_COLLECTED_YET"
	CodeTokenDecryptFail = "TOKEN_DECRYPT_FAILED"
	CodeTokenInvalid     = "TOKEN_INVALID"
	CodeNetwork          = "NETWORK_ERROR"
	CodeServerError      = "SERVER_ERROR"
	CodeRateLimited      = "RATE_LIMITED"
	CodeClientError      = "CLIENT_ERROR"
	CodeBizError         = "BIZ_ERROR"
)

// ProviderStatus 取值。
const (
	StatusOK           = "ok"
	StatusFailed       = "failed"
	StatusTokenInvalid = "token_invalid"
)

// ProviderState 快照内单 provider 渲染态（不可变，整体替换）。
type ProviderState struct {
	ID            string
	Name          string
	Status        string // ok | failed | token_invalid
	Data          json.RawMessage
	LastSuccessAt *time.Time
	Error         *ErrorInfo
}

// Snapshot 完整快照。
type Snapshot struct {
	Revision    uint64
	CollectedAt time.Time
	Version     string
	Providers   []ProviderState
}

// ---------- wire 序列化（RFC3339 UTC 秒级，契约增量 #2；data 为 JSON 值：对象或字符串） ----------

type providerWire struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Status        string          `json:"status"`
	Data          json.RawMessage `json:"data"`
	LastSuccessAt *string         `json:"last_success_at"`
	Error         *ErrorInfo      `json:"error,omitempty"`
}

type snapshotWire struct {
	Revision    uint64         `json:"revision"`
	CollectedAt string         `json:"collected_at"`
	Version     string         `json:"version"`
	Providers   []providerWire `json:"providers"`
}

// FormatTime 时间戳序列化：RFC3339、UTC、秒级（Z 结尾无小数）。
func FormatTime(t time.Time) string {
	return t.Truncate(time.Second).UTC().Format(time.RFC3339)
}

// MarshalJSON 实现 /api/quotas 响应形态。
func (s *Snapshot) MarshalJSON() ([]byte, error) {
	w := snapshotWire{
		Revision:    s.Revision,
		CollectedAt: FormatTime(s.CollectedAt),
		Version:     s.Version,
		Providers:   make([]providerWire, 0, len(s.Providers)),
	}
	for i := range s.Providers {
		p := &s.Providers[i]
		pw := providerWire{
			ID:     p.ID,
			Name:   p.Name,
			Status: p.Status,
			Data:   p.Data, // nil → null；字符串透传时为带引号字符串
			Error:  p.Error,
		}
		if p.LastSuccessAt != nil {
			f := FormatTime(*p.LastSuccessAt)
			pw.LastSuccessAt = &f
		}
		w.Providers = append(w.Providers, pw)
	}
	return json.Marshal(w)
}

// ---------- store ----------

// Store 快照仓库：atomic.Pointer 发布不可变快照，读方无锁（设计 D-1）。
type Store struct {
	ptr     atomic.Pointer[Snapshot]
	version string
}

// NewStore 初始化 revision=0 的空快照。
func NewStore(version string) *Store {
	s := &Store{version: version}
	s.ptr.Store(&Snapshot{Revision: 0, CollectedAt: time.Now(), Version: version, Providers: []ProviderState{}})
	return s
}

// Get 加载当前不可变快照。
func (s *Store) Get() *Snapshot { return s.ptr.Load() }

// Publish 以新 provider 状态重建快照：与当前快照 diff（§9.2 五类触发），
// 任一变化 → revision+1 且 collected_at=now（严格同节奏）；无变化不发布（指针不动，GET 逐字节恒定）。
// 单次重建至多 +1（C-snap-09：同 tick 多变化合并）。返回是否发布。
func (s *Store) Publish(now time.Time, states []ProviderState) bool {
	old := s.ptr.Load()
	if old != nil && !snapshotChanged(old, states) {
		return false
	}
	provs := append([]ProviderState(nil), states...)
	if provs == nil {
		provs = []ProviderState{}
	}
	var rev uint64 = 1
	if old != nil {
		rev = old.Revision + 1
	}
	s.ptr.Store(&Snapshot{
		Revision:    rev,
		CollectedAt: now.UTC(),
		Version:     s.version,
		Providers:   provs,
	})
	return true
}

// snapshotChanged 五类触发 diff（T1.5 核心）：
// ① 任一 data 字节不等 ② 任一 status 不等 ③ error 内容不等（code/message/retry_after_s 任一）
// ④ 任一 last_success_at 刷新（每次成功必刷 → 值必变）⑤ 快照结构变化（providers 增删/改名/换序）。
// 不变量：响应一变 revision 必变，响应没变 revision 必不动。
// 守门规则：ProviderState 新增渲染字段时必须同步进本函数，否则 diff 漏报（拆解文档 §7 风险条目）。
func snapshotChanged(old *Snapshot, states []ProviderState) bool {
	if len(old.Providers) != len(states) {
		return true // ⑤ 结构变化：增删
	}
	for i := range states {
		o, n := &old.Providers[i], &states[i]
		if o.ID != n.ID || o.Name != n.Name {
			return true // ⑤ 结构变化：id/name 变化或换序
		}
		if o.Status != n.Status {
			return true // ②
		}
		if !bytes.Equal(o.Data, n.Data) {
			return true // ①
		}
		if !sameTime(o.LastSuccessAt, n.LastSuccessAt) {
			return true // ④（成功刷新 last_success_at 即触发）
		}
		if !sameError(o.Error, n.Error) {
			return true // ③
		}
	}
	return false
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Unix() == b.Unix()
}

func sameError(a, b *ErrorInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Code != b.Code || a.Message != b.Message {
		return false
	}
	if a.RetryAfterS == nil || b.RetryAfterS == nil {
		return a.RetryAfterS == b.RetryAfterS
	}
	return *a.RetryAfterS == *b.RetryAfterS
}
