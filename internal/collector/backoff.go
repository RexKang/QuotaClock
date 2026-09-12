package collector

import "time"

// Backoff 每 provider 独立的退避状态机（设计 §7.4）：
// 连续失败计数 n → delay = min(base × mult^n, max)（n 从 0 起：首次失败 = base，
// 序列 300→600→1200→1800→1800…）；成功一次复位；429 按 Retry-After 顺延（无头用当前退避值，
// 计数不增）；401/403 停采直到热更重建。退避基数用 interval_base_s（非上一间隔）。
type Backoff struct {
	fails         int
	nextAttemptAt time.Time
	stopped       bool
}

// backoffDelay 计算 base × mult^pow，封顶 max（单位秒）。
func backoffDelay(baseS, mult, maxS, pow int) time.Duration {
	d := time.Duration(baseS) * time.Second
	cap := time.Duration(maxS) * time.Second
	for i := 0; i < pow; i++ {
		d *= time.Duration(mult)
		if d >= cap {
			return cap
		}
	}
	if d > cap {
		d = cap
	}
	return d
}

// OnFailure 记录一次失败，返回下次尝试前应等待的时长。
func (b *Backoff) OnFailure(now time.Time, baseS, mult, maxS int) time.Duration {
	d := backoffDelay(baseS, mult, maxS, b.fails)
	b.fails++
	b.nextAttemptAt = now.Add(d)
	return d
}

// OnRateLimited 429 处置：按 Retry-After 顺延；无头用当前退避值（当前生效延迟，
// 即上次失败设定的值；失败计数不增）。
func (b *Backoff) OnRateLimited(now time.Time, retryAfterS *int, baseS, mult, maxS int) time.Duration {
	var d time.Duration
	if retryAfterS != nil {
		d = time.Duration(*retryAfterS) * time.Second
	} else {
		pow := b.fails
		if pow > 0 {
			pow-- // 当前生效值 = 上次失败所设延迟
		}
		d = backoffDelay(baseS, mult, maxS, pow)
	}
	b.nextAttemptAt = now.Add(d)
	return d
}

// OnSuccess 成功：计数清零、nextAttemptAt 清零（立即恢复基准间隔）。
// 用于**平台级**退避：平台恢复后立刻解开全部 key 的闸门，把调度节奏交回 key 自己
// （key 级周期由 OnSuccessAt 排期；若这里也排期，会让同平台第二个 key 的等分偏移被推迟一轮）。
func (b *Backoff) OnSuccess() {
	b.fails = 0
	b.nextAttemptAt = time.Time{}
}

// OnSuccessAt 成功并按周期排下一次（v0.2.5 r4.1）：计数清零 + nextAttemptAt = now + period。
//
// 为什么 key 级不能像平台级那样清零：主循环除周期性 tick 外，还会被「保存配置」唤醒
// （Reload → wake → 立即 runTick）。清成零时任何一次额外 tick 都会把已经采到的 key 再采一遍
// ——用户实测：只改了一个 API Key 的名称，采集也整体重跑了一轮（状态虽然保留，
// 但上游实打实多打了一次）。按「该 key 的采集周期」排期后，热更不打断既有节奏。
// 从未采集过的 key、以及 token/地址等定义变更后重建的 key（&Backoff{} 零值）不受影响：仍然立即采集。
func (b *Backoff) OnSuccessAt(now time.Time, period time.Duration) {
	b.fails = 0
	b.nextAttemptAt = now.Add(period)
}

// Stop token 失效停采：不参与后续调度，直到热更重建该 provider。
func (b *Backoff) Stop() { b.stopped = true }

// Stopped 是否停采态。
func (b *Backoff) Stopped() bool { return b.stopped }

// Eligible 本 tick 是否应采集：非停采且 now ≥ nextAttemptAt（退避/429 顺延未到则跳过）。
func (b *Backoff) Eligible(now time.Time) bool {
	return !b.stopped && !now.Before(b.nextAttemptAt)
}

// NextAttemptAt 返回下次尝试时刻（测试/日志用）。
func (b *Backoff) NextAttemptAt() time.Time { return b.nextAttemptAt }
