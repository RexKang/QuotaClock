// Package logx 提供 QuotaClock 的 stdout 单行日志与脱敏红线。
//
// 格式：2006-01-02 15:04:05 [LEVEL] msg
// 级别：DEBUG < INFO < WARN < ERROR；默认 INFO，环境变量 QUOTACLOCK_LOG=debug 提升至 DEBUG。
//
// 脱敏红线（PRD §11-17）：token 明文/密文/掩码、Authorization/Cookie 值、会话 cookie 值
// 永不出现在日志中。实现为两层：
//  1. RegisterSecret：代码在拿到敏感值（token 明文、密文、掩码、会话 cookie）时登记，
//     Redact 会把后续日志中出现的这些字面量替换为 ***（兜底，防拼接泄漏）；
//  2. 代码纪律：采集失败日志不打响应体，登录失败只打 IP+次数。
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	LevelDebug = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	mu      sync.RWMutex
	level   = LevelInfo
	secrets []string
	out     io.Writer = os.Stdout
)

func init() {
	if strings.EqualFold(os.Getenv("QUOTACLOCK_LOG"), "debug") {
		level = LevelDebug
	}
}

// SetLevel 供测试注入；生产路径由 init 依环境变量决定。
func SetLevel(l int) { mu.Lock(); level = l; mu.Unlock() }

// SetOutput 重定向日志输出（测试注入用；生产恒为 stdout）。
func SetOutput(w io.Writer) { mu.Lock(); out = w; mu.Unlock() }

// RegisterSecret 登记一个敏感字面量（≥4 字节才登记，避免过短串误伤正文）。
func RegisterSecret(s string) {
	if len(s) < 4 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, old := range secrets {
		if old == s {
			return
		}
	}
	secrets = append(secrets, s)
}

// Redact 把消息中出现的已登记敏感字面量替换为 ***。
func Redact(msg string) string {
	mu.RLock()
	defer mu.RUnlock()
	for _, s := range secrets {
		if strings.Contains(msg, s) {
			msg = strings.ReplaceAll(msg, s, "***")
		}
	}
	return msg
}

func logf(lv int, name, format string, args ...any) {
	mu.RLock()
	enabled := lv >= level
	mu.RUnlock()
	if !enabled {
		return
	}
	msg := Redact(fmt.Sprintf(format, args...))
	fmt.Fprintf(out, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), name, msg)
}

func Debugf(format string, args ...any) { logf(LevelDebug, "DEBUG", format, args...) }

// Debug 输出无格式化参数的 DEBUG 行。
func Debug(msg string)                  { logf(LevelDebug, "DEBUG", "%s", msg) }
func Infof(format string, args ...any)  { logf(LevelInfo, "INFO", format, args...) }
func Warnf(format string, args ...any)  { logf(LevelWarn, "WARN", format, args...) }
func Errorf(format string, args ...any) { logf(LevelError, "ERROR", format, args...) }
