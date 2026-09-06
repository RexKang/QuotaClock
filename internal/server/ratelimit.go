package server

import (
	"sync"
	"time"
)

// 登录限速参数：全局 5 次失败锁 10 分钟；锁定期内正确密码也一律拒绝（PRD §6.2）。
// 内存态，重启清零（限速仅防暴力撞库，非安全边界）。
const (
	MaxLoginFails = 5
	LoginLockTime = 10 * time.Minute
)

// Limiter 全局登录失败限速器。
type Limiter struct {
	mu          sync.Mutex
	fails       int
	lockedUntil time.Time
	clock       func() time.Time
}

// NewLimiter 构造（clock 可注入测试）。
func NewLimiter() *Limiter { return &Limiter{clock: time.Now} }

// Allow 当前是否允许尝试登录；锁定期内返回剩余等待时长。
func (l *Limiter) Allow() (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if !l.lockedUntil.IsZero() {
		if now.Before(l.lockedUntil) {
			return false, l.lockedUntil.Sub(now)
		}
		// 锁定期结束：计数清零
		l.lockedUntil = time.Time{}
		l.fails = 0
	}
	return true, 0
}

// Fail 记录一次失败；达 5 次触发全局锁定 10 分钟。
func (l *Limiter) Fail() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails++
	if l.fails >= MaxLoginFails {
		l.lockedUntil = l.clock().Add(LoginLockTime)
	}
}

// Success 登录成功：计数清零。
func (l *Limiter) Success() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails = 0
	l.lockedUntil = time.Time{}
}
