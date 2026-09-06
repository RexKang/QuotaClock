package collector

import (
	"testing"
	"time"
)

func TestBackoffSequence(t *testing.T) { // C-col-02：300→600→1200→1800→1800；成功复位
	b := &Backoff{}
	now := time.Unix(0, 0)
	base, mult, maxS := 300, 2, 1800
	want := []time.Duration{300, 600, 1200, 1800, 1800}
	for i, w := range want {
		d := b.OnFailure(now, base, mult, maxS)
		if d != w*time.Second {
			t.Fatalf("第 %d 次失败 delay = %v, want %v", i+1, d, w*time.Second)
		}
		if b.NextAttemptAt() != now.Add(d) {
			t.Fatalf("nextAttemptAt 错误")
		}
	}
	b.OnSuccess()
	if b.fails != 0 || !b.nextAttemptAt.IsZero() {
		t.Fatal("成功未复位")
	}
	if d := b.OnFailure(now, base, mult, maxS); d != 300*time.Second {
		t.Fatalf("复位后应为 300: %v", d)
	}
}

func TestBackoffRateLimit(t *testing.T) { // C-col-03：Retry-After=120 → now+120s；无头 = 当前退避值
	b := &Backoff{}
	now := time.Unix(0, 0)
	// 已有 2 次失败（当前退避值 = 600）
	b.OnFailure(now, 300, 2, 1800)
	b.OnFailure(now, 300, 2, 1800)
	r120 := 120
	d := b.OnRateLimited(now, &r120, 300, 2, 1800)
	if d != 120*time.Second || b.NextAttemptAt() != now.Add(120*time.Second) {
		t.Fatalf("Retry-After 顺延错误: %v", d)
	}
	if b.fails != 2 {
		t.Fatalf("429 不应增加失败计数: %d", b.fails)
	}
	// 无头 → 当前退避值 600，计数仍不增
	d = b.OnRateLimited(now, nil, 300, 2, 1800)
	if d != 600*time.Second {
		t.Fatalf("无头应用当前退避值: %v", d)
	}
	if b.fails != 2 {
		t.Fatalf("计数应保持 2: %d", b.fails)
	}
}

func TestBackoffTokenInvalidStop(t *testing.T) { // C-col-04：401 停采，不参与后续调度
	b := &Backoff{}
	now := time.Unix(0, 0)
	b.OnFailure(now, 300, 2, 1800)
	b.Stop()
	if !b.Stopped() || b.Eligible(now) {
		t.Fatal("停采后不应可调度")
	}
	if b.Eligible(now.Add(100 * time.Hour)) {
		t.Fatal("停采与时间无关")
	}
}

func TestBackoffEligible(t *testing.T) {
	b := &Backoff{}
	now := time.Unix(0, 0)
	if !b.Eligible(now) {
		t.Fatal("初始应可调度")
	}
	b.OnFailure(now, 300, 2, 1800)
	if b.Eligible(now.Add(299 * time.Second)) {
		t.Fatal("退避期内不应调度")
	}
	if !b.Eligible(now.Add(300 * time.Second)) {
		t.Fatal("退避到期应可调度")
	}
}

func TestBackoffOverflowCap(t *testing.T) { // 大 base × 大 mult 不溢出
	b := &Backoff{}
	now := time.Unix(0, 0)
	var d time.Duration
	for i := 0; i < 40; i++ {
		d = b.OnFailure(now, 86400, 8, 86400)
	}
	if d != 86400*time.Second {
		t.Fatalf("封顶错误: %v", d)
	}
}
