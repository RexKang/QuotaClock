package collector

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RexKang/QuotaClock/internal/config"
	"github.com/RexKang/QuotaClock/internal/logx"
)

// Scheduler 采集调度器：单 goroutine 串行 tick 循环（设计 §7.1），
// tick 内各平台按累计 stagger 延迟错峰并发启动、执行互不等待（D-10）。
type Scheduler struct {
	cfg    atomic.Pointer[config.Runtime]
	store  *Store
	client *Client

	mu       sync.Mutex
	defs     map[string]*config.RuntimeProvider
	states   map[string]*ProviderState
	backoffs map[string]*Backoff

	cache CacheWriter // 上次成功数据落盘出口（nil = 不启用缓存，v0.2.4）

	wake chan struct{} // 热生效唤醒：配置保存后尽快开跑下一 tick（验收 #9 ≤1 轮询周期红字的前提）

	clock  func() time.Time
	randFn func(n int) int // [0,n)

	runWG  sync.WaitGroup // Run 主循环
	tickWG sync.WaitGroup // 在途 fetch（优雅退出等待，25s 出站超时封顶）
}

// NewScheduler 初始化并发布首个快照（全部 provider = 尚未采集态；revision 0 → 1）。
func NewScheduler(cfg *config.Runtime, store *Store, client *Client) *Scheduler {
	s := &Scheduler{
		store:    store,
		client:   client,
		defs:     map[string]*config.RuntimeProvider{},
		states:   map[string]*ProviderState{},
		backoffs: map[string]*Backoff{},
		wake:     make(chan struct{}, 1),
		clock:    time.Now,
		randFn:   defaultRand,
	}
	s.cfg.Store(cfg)
	s.mu.Lock()
	s.reloadLocked(cfg)
	s.mu.Unlock()
	return s
}

func defaultRand(n int) int { return rand.IntN(n) }

// SetCacheWriter 挂载缓存落盘出口（main 装配；nil = 不写缓存）。
func (s *Scheduler) SetCacheWriter(w CacheWriter) {
	s.mu.Lock()
	s.cache = w
	s.mu.Unlock()
}

// SeedCached 用 cache.json 恢复的状态填充「尚未完成首次采集」的空态（v0.2.4，启动时调用一次）：
// 只覆盖未采集态（NOT_COLLECTED_YET）与既有缓存态，绝不覆盖真实采集结果。
func (s *Scheduler) SeedCached(cached []ProviderState) int {
	if len(cached) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i := range cached {
		st := cached[i]
		if s.defs[st.ID] == nil {
			continue // 配置里已不存在
		}
		cur := s.states[st.ID]
		if cur == nil {
			continue
		}
		notCollected := cur.Status == StatusFailed && cur.Error != nil && cur.Error.Code == CodeNotCollectedYet
		if cur.Status != StatusCached && !notCollected {
			continue // 已有真实结果，缓存不覆盖
		}
		cp := st
		s.states[st.ID] = &cp
		n++
	}
	if n > 0 {
		s.publishLocked()
	}
	return n
}

// Reload 热生效：换配置指针并重建调度状态（设计 §11）——
// 未变 provider 保留退避计数与最近结果；变更/新增重置；被删的取消调度；
// 重建期间在途 fetch 照常完成，数据归属已删 id 的丢弃。
func (s *Scheduler) Reload(cfg *config.Runtime) {
	s.mu.Lock()
	s.cfg.Store(cfg)
	s.reloadLocked(cfg)
	s.saveCacheLocked() // 平台删除/停用后缓存立即收敛（不在 states 中即被剔出）
	s.mu.Unlock()
	logx.Infof("采集器已按新配置重建（%d 个平台）", len(cfg.Providers))
	// 唤醒主循环：下一 tick 立即开跑（新配置即刻参与采集；验收 #9 失败态传播时限的前提）
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) reloadLocked(cfg *config.Runtime) {
	newDefs := make(map[string]*config.RuntimeProvider, len(cfg.Providers))
	for _, p := range cfg.Providers {
		newDefs[p.ID] = p
		if old := s.defs[p.ID]; old != nil && !p.DecryptFailed &&
			s.states[p.ID] != nil && s.backoffs[p.ID] != nil && sameDefinition(old, p) {
			continue // 未变：保留退避与历史
		}
		st := &ProviderState{ID: p.ID, Name: p.Name}
		if p.DecryptFailed {
			// key.bin 丢失/不匹配的降级（设计 §6.4）：token_invalid，不 crash
			st.Status = StatusTokenInvalid
			st.Error = &ErrorInfo{Code: CodeTokenDecryptFail, Message: "token 无法解密，请在设置中重新录入"}
		} else if !p.IsEnabled() {
			// v0.2.4：停用平台灰显，错误码 DISABLED（前端识别渲染「已停用」徽标）
			st.Status = StatusDisabled
			st.Error = &ErrorInfo{Code: CodeDisabled, Message: "平台已停用"}
		} else {
			st.Status = StatusFailed
			st.Error = &ErrorInfo{Code: CodeNotCollectedYet, Message: "尚未完成首次采集"}
		}
		s.states[p.ID] = st
		s.backoffs[p.ID] = &Backoff{}
	}
	s.defs = newDefs
	for id := range s.states {
		if _, ok := newDefs[id]; !ok {
			delete(s.states, id)
			delete(s.backoffs, id)
		}
	}
	s.publishLocked()
}

// sameDefinition 判断 provider 定义是否变化（token 变 → TokenCipher 变 → 重置）。
// enabled 也算定义变化：勾选/取消停用要立刻重建状态，否则卡片不会实时显示「已停用」（v0.2.4）。
func sameDefinition(a, b *config.RuntimeProvider) bool {
	if a.Name != b.Name || a.BaseURL != b.BaseURL || a.AuthStyle != b.AuthStyle || a.TokenCipher != b.TokenCipher {
		return false
	}
	if a.IsEnabled() != b.IsEnabled() {
		return false
	}
	if len(a.Paths) != len(b.Paths) {
		return false
	}
	for i := range a.Paths {
		if a.Paths[i] != b.Paths[i] {
			return false
		}
	}
	if len(a.ExtraHeaders) != len(b.ExtraHeaders) {
		return false
	}
	for k, v := range a.ExtraHeaders {
		if b.ExtraHeaders[k] != v {
			return false
		}
	}
	return true
}

// Run 调度主循环：runTick → sleep(max(0, base+jitter − elapsed))，锚定 tick 开始时刻。
// 配置热生效会唤醒休眠中的主循环，立即以新配置进入下一 tick。
func (s *Scheduler) Run(ctx context.Context) {
	s.runWG.Add(1)
	defer s.runWG.Done()
	for {
		tickStart := s.clock()
		s.runTick(ctx)
		if ctx.Err() != nil {
			return
		}
		cfg := s.cfg.Load()
		jitter := cfg.Collector.JitterMinS
		if span := cfg.Collector.JitterMaxS - cfg.Collector.JitterMinS; span > 0 {
			jitter += s.randFn(span + 1)
		}
		period := time.Duration(cfg.Collector.IntervalBaseS+jitter) * time.Second
		sleep := period - s.clock().Sub(tickStart)
		if sleep < 0 {
			sleep = 0
		}
		logx.Debugf("下一 tick 约 %s 后（基准 %ds + 抖动 %ds）", sleep, cfg.Collector.IntervalBaseS, jitter)
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
			logx.Debug("配置热生效唤醒，立即进入下一 tick")
		case <-timer.C:
		}
	}
}

// Wait 等待 Run 主循环结束（含最后一个 tick 的全部在途 fetch）。
func (s *Scheduler) Wait() { s.runWG.Wait() }

// runTick 对「应采集」的 provider（非停采态且退避未到期）按累计错峰延迟并发发起采集，
// 并等待本 tick 全部 fetch 结束（§7.3）。
func (s *Scheduler) runTick(ctx context.Context) {
	s.mu.Lock()
	cfg := s.cfg.Load()
	jobs := s.planTickLocked(cfg)
	s.mu.Unlock()

	for _, j := range jobs {
		s.tickWG.Add(1)
		go func(j job) {
			defer s.tickWG.Done()
			if j.delay > 0 {
				t := time.NewTimer(j.delay)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
			if ctx.Err() != nil {
				return
			}
			s.collectProvider(ctx, j.p)
		}(j)
	}
	s.tickWG.Wait()
}

type job struct {
	p     *config.RuntimeProvider
	delay time.Duration
}

// planTickLocked 计算「应采集」集合：跳过停用/解密失败态 且 退避未到期；
// 第 k 个平台的启动延迟 = 前 k-1 项 rand(stagger) 之和（首个平台立即启动，C-col-05）。
func (s *Scheduler) planTickLocked(cfg *config.Runtime) []job {
	now := s.clock()
	var jobs []job
	acc := time.Duration(0)
	for _, p := range cfg.Providers {
		if p.DecryptFailed {
			continue // token 无法解密：停采态
		}
		if !p.IsEnabled() {
			continue // v0.2.4：平台停用（enabled=false）不参与采集
		}
		b := s.backoffs[p.ID]
		if b == nil || !b.Eligible(now) {
			logx.Debugf("跳过 %s（停采或退避未到）", p.ID)
			continue
		}
		start := acc
		acc += time.Duration(s.randStagger(cfg)) * time.Second
		jobs = append(jobs, job{p, start})
	}
	return jobs
}

// randStagger 抽取 stagger 区间 [min, max]（秒），禁写死常量、取自运行时 config。
func (s *Scheduler) randStagger(cfg *config.Runtime) int {
	span := cfg.Collector.StaggerMaxS - cfg.Collector.StaggerMinS
	if span < 0 {
		span = 0
	}
	return s.randFn(span+1) + cfg.Collector.StaggerMinS
}

type pathResult struct {
	path string
	res  Classified
}

// collectProvider 采集单个 provider：全部 path 并发请求 → 分类聚合 → 更新状态并发布快照。
func (s *Scheduler) collectProvider(ctx context.Context, p *config.RuntimeProvider) {
	t0 := s.clock()
	results := make([]pathResult, len(p.Paths))
	var wg sync.WaitGroup
	for i := range p.Paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = pathResult{path: p.Paths[i], res: s.client.FetchOnce(ctx, p, p.Paths[i])}
		}(i)
	}
	wg.Wait()
	elapsed := s.clock().Sub(t0)

	var okIdx []int
	var tokenInvalid, rateLimited, otherErr *pathResult
	for i := range results {
		r := &results[i]
		switch r.res.Class {
		case ClassOK:
			okIdx = append(okIdx, i)
		case ClassTokenInvalid:
			if tokenInvalid == nil {
				tokenInvalid = r
			}
		case ClassRateLimited:
			if rateLimited == nil {
				rateLimited = r
			}
		default:
			if otherErr == nil {
				otherErr = r
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg.Load()
	if cfg.Provider(p.ID) == nil {
		logx.Debugf("采集结果丢弃：%s 已从配置删除", p.ID)
		return
	}
	b := s.backoffs[p.ID]
	if b == nil {
		b = &Backoff{}
		s.backoffs[p.ID] = b
	}
	prior := s.states[p.ID]

	st := ProviderState{ID: p.ID, Name: p.Name}
	switch {
	case tokenInvalid != nil:
		// 401/403：停采该平台，不做退避（退避会掩盖 token 失效）
		b.Stop()
		st.Status = StatusTokenInvalid
		st.Error = &ErrorInfo{Code: CodeTokenInvalid, Message: tokenInvalid.res.Message}
		logx.Warnf("采集 %s：token 失效，已停采", p.ID)
	case len(okIdx) > 0:
		now := s.clock()
		b.OnSuccess()
		st.Status = StatusOK
		st.Data = mergePathData(results, okIdx)
		st.LastSuccessAt = &now
		st.Error = nil
		logx.Debugf("采集 %s 成功（%s）", p.ID, elapsed)
	case rateLimited != nil:
		// 429：按 Retry-After 顺延（无头用当前退避值）
		now := s.clock()
		delay := b.OnRateLimited(now, rateLimited.res.RetryAfterS,
			cfg.Collector.IntervalBaseS, cfg.Collector.BackoffMultiplier, cfg.Collector.BackoffMaxS)
		st.Status = StatusFailed
		st.Data = priorData(prior)
		st.LastSuccessAt = priorSuccess(prior)
		rs := int(delay / time.Second)
		st.Error = &ErrorInfo{Code: CodeRateLimited, Message: rateLimited.res.Message, RetryAfterS: &rs}
		logx.Warnf("采集 %s：429 限流，%ds 后重试", p.ID, rs)
	default:
		// 其余失败：指数退避
		now := s.clock()
		delay := b.OnFailure(now, cfg.Collector.IntervalBaseS, cfg.Collector.BackoffMultiplier, cfg.Collector.BackoffMaxS)
		st.Status = StatusFailed
		st.Data = priorData(prior)
		st.LastSuccessAt = priorSuccess(prior)
		rs := int(delay / time.Second)
		st.Error = &ErrorInfo{Code: classCode(otherErr.res.Class), Message: otherErr.res.Message, RetryAfterS: &rs}
		logx.Warnf("采集 %s：%s，%ds 后重试", p.ID, otherErr.res.Class, rs)
	}
	s.states[p.ID] = &st
	s.publishLocked()
	if st.Status == StatusOK {
		// 只有真实成功才刷新缓存（失败态同上：保留上次成功数据，不覆写）
		s.saveCacheLocked()
	}
}

// saveCacheLocked 用当前状态集投递缓存快照（调用方持锁；非阻塞，落盘在后台 goroutine）。
func (s *Scheduler) saveCacheLocked() {
	if s.cache == nil {
		return
	}
	cfg := s.cfg.Load()
	states := make([]ProviderState, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if st := s.states[p.ID]; st != nil {
			states = append(states, *st)
		}
	}
	s.cache.Save(CacheFromStates(s.clock(), states))
}

func priorData(p *ProviderState) json.RawMessage {
	if p == nil {
		return nil
	}
	return p.Data
}

func priorSuccess(p *ProviderState) *time.Time {
	if p == nil {
		return nil
	}
	return p.LastSuccessAt
}

func classCode(c ErrClass) string {
	switch c {
	case ClassNetwork:
		return CodeNetwork
	case ClassServerErr:
		return CodeServerError
	case ClassClientErr:
		return CodeClientError
	case ClassBizError:
		return CodeBizError
	default:
		return CodeNetwork
	}
}

// mergePathData 多 path 成功结果的合并：单成功 → 原文；多成功 → JSON 对象深合并
// （键冲突先到路径优先；非对象结果丢弃，DEBUG 可查）。空值不变量 §9.3 的 data 形态由此保证。
func mergePathData(results []pathResult, okIdx []int) json.RawMessage {
	if len(okIdx) == 1 {
		return results[okIdx[0]].res.Data
	}
	var acc map[string]json.RawMessage
	for _, i := range okIdx {
		var m map[string]json.RawMessage
		if json.Unmarshal(results[i].res.Data, &m) != nil || m == nil {
			continue
		}
		if acc == nil {
			acc = m
			continue
		}
		mergeMaps(acc, m)
	}
	if acc == nil {
		return results[okIdx[0]].res.Data
	}
	b, err := json.Marshal(acc)
	if err != nil {
		return results[okIdx[0]].res.Data
	}
	return b
}

// mergeMaps 把 b 深合并进 a：键冲突且双方均为对象 → 递归合并；否则保留 a（先到路径优先）。
func mergeMaps(a, b map[string]json.RawMessage) {
	for k, v := range b {
		if av, ok := a[k]; ok {
			var am, bm map[string]json.RawMessage
			if json.Unmarshal(av, &am) == nil && json.Unmarshal(v, &bm) == nil && am != nil && bm != nil {
				mergeMaps(am, bm)
				if nb, err := json.Marshal(am); err == nil {
					a[k] = nb
				}
			}
			continue
		}
		a[k] = v
	}
}

// publishLocked 按 cfg 顺序重建完整快照并发布（diff 无变化则不发布，revision 不动）。
func (s *Scheduler) publishLocked() {
	cfg := s.cfg.Load()
	states := make([]ProviderState, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if st := s.states[p.ID]; st != nil {
			states = append(states, *st)
		}
	}
	s.store.Publish(s.clock(), states)
}
